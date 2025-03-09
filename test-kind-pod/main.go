package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	envDPUNodeType             = "DPU_NODE_TYPE"
	envInfraClusterConfig      = "TEST_CLUSTER_KUBECONFIG"
	envDPUClusterToken         = "DPU_CLUSTER_TOKEN"
	envDPUClusterHash          = "DPU_CLUSTER_HASH"
	envDPUControlPlaneEndpoint = "DPU_CLUSTER_ENDPOINT"
)

func main() {
	// get Kubernetes config
	config, err := getKubeConfig()
	if err != nil {
		fmt.Printf("Failed to get kubeconfig: %v\n", err)
		os.Exit(1)
	}

	// create the clinet
	k8sClient, err := client.New(config, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		fmt.Printf("Failed to create client: %v\n", err)
		os.Exit(1)
	}

	podName, err := getPodName()
	if err != nil {
		podName = "kind-example"
	}

	// define the Pod
	podNamespace := "default"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: podNamespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "kind-container",
					Image: "kindest/node:v1.30.0",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),   // 请求 100 毫核 CPU
							corev1.ResourceMemory: resource.MustParse("4096Mi"), // 请求 256 MiB 内存
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),   // 限制 500 毫核 CPU
							corev1.ResourceMemory: resource.MustParse("4096Mi"), // 限制 512 MiB 内存
						},
					},
					SecurityContext: &corev1.SecurityContext{
						Privileged: &[]bool{true}[0],
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeUnconfined,
						},
						Capabilities: &corev1.Capabilities{
							Add: []corev1.Capability{
								"SYS_ADMIN",
							},
						},
					},
					ImagePullPolicy: corev1.PullIfNotPresent,
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "lib-modules",
							MountPath: "/lib/modules",
							ReadOnly:  true,
						},
						{
							Name:      "run-tmpfs",
							MountPath: "/run",
						},
						{
							Name:      "tmp-tmpfs",
							MountPath: "/tmp",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "lib-modules",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/lib/modules",
							Type: &[]corev1.HostPathType{corev1.HostPathDirectory}[0],
						},
					},
				},
				{
					Name: "run-tmpfs",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{
							Medium: corev1.StorageMediumMemory,
						},
					},
				},
				{
					Name: "tmp-tmpfs",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{
							Medium: corev1.StorageMediumMemory,
						},
					},
				},
			},
		},
	}

	// create Pod
	ctx := context.Background()
	err = k8sClient.Create(ctx, pod)
	if err != nil {
		fmt.Printf("Failed to create pod: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Pod creation request sent successfully.")

	// wait Pod get running
	for {
		var pod corev1.Pod
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: podNamespace, Name: podName}, &pod)
		if err != nil {
			log.Fatalf("Failed to get pod: %v", err)
		}
		if pod.Status.Phase == corev1.PodRunning {
			fmt.Printf("Pod %s is running\n", pod.Name)
			break
		}
		time.Sleep(2 * time.Second)
	}

	joinDPUCluster(ctx, config, podName, podNamespace)

}

func joinDPUCluster(ctx context.Context, config *rest.Config, podName, podNamespace string) {
	// 在 Pod 中执行 kubeadm 命令
	/*
		cmd := []string{
			"touch /tmp/test_file",
	}*/
	endpoint := os.Getenv(envDPUControlPlaneEndpoint)
	token := os.Getenv(envDPUClusterToken)
	hash := os.Getenv(envDPUClusterHash)
	//klog.Infof("DPUClusterEndPoint:%s, Token:%s, HashCode: %s", endpoint, token, hash)
	fmt.Printf("DPUClusterEndPoint:%s, Token:%s, HashCode: %s\n", endpoint, token, hash)

	cmd := []string{
		"kubeadm",
		"join",
		endpoint,
		"--token",
		token,
		"--discovery-token-ca-cert-hash",
		hash,
	}
	/*
		cmd := []string{
			"kubeadm",
			"join",
			"172.18.0.2:6443",
			"--token",
			"abcdef.0123456789abcdef",
			"--discovery-token-ca-cert-hash",
			"sha256:3691a18d1353ba2d280ec60805a32adc33b0e65639262b6421c0c7738cae5b4f",
		}*/

	// 构建 REST 请求
	gvk := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
	restConfig := *config
	groupVersion := gvk.GroupVersion()
	restConfig.GroupVersion = &groupVersion
	restConfig.APIPath = "/api"
	restConfig.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	restClient, err := rest.RESTClientFor(&restConfig)
	if err != nil {
		log.Fatalf("Failed to create REST client: %v", err)
	}

	podExecOptions := &corev1.PodExecOptions{
		Command:   cmd,
		Container: "kind-container",
		Stdin:     false,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}

	req := restClient.Post().
		Namespace(podNamespace).
		Resource("pods").
		Name(podName).
		SubResource("exec").
		VersionedParams(podExecOptions, scheme.ParameterCodec)
		//VersionedParams(podExecOptions, metav1.ParameterCodec)

	// 确保请求头包含升级信息
	req.SetHeader("Connection", "Upgrade")
	req.SetHeader("Upgrade", "SPDY/3.1")

	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		fmt.Printf("Failed to create executor:%v", err)
		log.Fatalf("Failed to create executor: %v", err)
	}

	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	go func() {
		defer stdoutW.Close()
		defer stderrW.Close()
		err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdout: stdoutW,
			Stderr: stderrW,
			Stdin:  nil,
		})
		if err != nil {
			log.Fatalf("Failed to execute command in pod: %v", err)

		}
	}()

	outputChan := make(chan string)
	stderrChan := make(chan string)

	go func() {
		output, err := io.ReadAll(stdoutR)
		if err != nil {
			log.Fatalf("Failed to read command output: %v", err)
		}
		log.Fatalf("the output is : %v", output)
		outputChan <- string(output)
	}()

	go func() {
		stderrOutput, err := io.ReadAll(stderrR)
		if err != nil {
			log.Fatalf("Failed to read command stderr: %v", err)
		}
		stderrChan <- string(stderrOutput)
	}()

	select {
	case output := <-outputChan:
		fmt.Printf("Command output:\n%s\n", output)
	case <-time.After(30 * time.Second):
		log.Println("Command output read timed out.")
	}

	select {
	case stderrOutput := <-stderrChan:
		fmt.Printf("Command stderr:\n%s\n", stderrOutput)
	case <-time.After(30 * time.Second):
		log.Println("Command stderr read timed out.")
	}
}

// getKubeConfig 获取 Kubernetes 配置
// func getKubeConfig() (*rest.Config, error) {
func getKubeConfig() (*rest.Config, error) {
	kubeconfig := os.Getenv(envInfraClusterConfig)
	fmt.Printf("Test cluster kubeconfig:%s", kubeconfig)
	if kubeconfig == "" {
		kubeconfig = clientcmd.RecommendedHomeFile
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)

}

// generate random string
func generateSecureRandomString(length int) (string, error) {
	result := make([]byte, length)
	for i := 0; i < length; i++ {
		var num uint32
		err := binary.Read(rand.Reader, binary.BigEndian, &num)
		if err != nil {
			return "", err
		}
		// 取模 26 得到 0 到 25 之间的随机数
		result[i] = byte(num%26 + 97)
	}
	return string(result), nil
}

func getPodName() (string, error) {
	length := 7
	randomStr, err := generateSecureRandomString(length)
	if err != nil {
		fmt.Printf("failed to generate random string for pod name: %v\n", err)
		return "", err
	}
	podName := fmt.Sprintf("kind-%s", randomStr)
	return podName, nil
}

// checkPodCreation 检查 Pod 是否创建成功
/*
func checkPodCreation(k8sClient client.Client, ctx context.Context, podName, podNamespace string) bool {
	var pod corev1.Pod
	// 最多尝试 30 秒，每 2 秒检查一次
	for i := 0; i < 15; i++ {
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: podNamespace, Name: podName}, &pod)
		if err == nil && pod.Status.Phase == corev1.PodRunning {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}
*/
