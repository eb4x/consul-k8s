package connectinject

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
)

const k8sNamespace = "k8snamespace"

func TestHandlerContainerInit(t *testing.T) {
	minimal := func() *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "test-namespace",
				Annotations: map[string]string{
					annotationService: "foo",
				},
			},

			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "web",
					},
					{
						Name: "web-side",
					},
				},
			},
			Status: corev1.PodStatus{
				HostIP: "1.1.1.1",
				PodIP:  "2.2.2.2",
			},
		}
	}

	cases := []struct {
		Name    string
		Pod     func(*corev1.Pod) *corev1.Pod
		Webhook MeshWebhook
		Cmd     string // Strings.Contains test
		CmdNot  string // Not contains
		ErrStr  string // Error contains
	}{
		// The first test checks the whole template. Subsequent tests check
		// the parts that change.
		{
			"Whole template by default",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				return pod
			},
			MeshWebhook{},
			`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=0s \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -bootstrap > /consul/connect-inject/envoy-bootstrap.yaml`,
			"",
			"",
		},

		{
			"When auth method is set -service-account-name and -service-name are passed in",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				pod.Spec.ServiceAccountName = "a-service-account-name"
				pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
					{
						Name:      "sa",
						MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
					},
				}
				return pod
			},
			MeshWebhook{
				AuthMethod:       "an-auth-method",
				ConsulAPITimeout: 5 * time.Second,
			},
			`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -acl-auth-method="an-auth-method" \
  -service-account-name="a-service-account-name" \
  -service-name="web" \
`,
			"",
			"",
		},
		{
			"When running the merged metrics server, configures consul connect envoy command",
			func(pod *corev1.Pod) *corev1.Pod {
				// The annotations to enable metrics, enable merging, and
				// service metrics port make the condition to run the merged
				// metrics server true. When that is the case,
				// prometheusScrapePath and mergedMetricsPort should get
				// rendered as -prometheus-scrape-path and
				// -prometheus-backend-port to the consul connect envoy command.
				pod.Annotations[annotationService] = "web"
				pod.Annotations[annotationEnableMetrics] = "true"
				pod.Annotations[annotationEnableMetricsMerging] = "true"
				pod.Annotations[annotationMergedMetricsPort] = "20100"
				pod.Annotations[annotationServiceMetricsPort] = "1234"
				pod.Annotations[annotationPrometheusScrapePort] = "22222"
				pod.Annotations[annotationPrometheusScrapePath] = "/scrape-path"
				pod.Annotations[annotationPrometheusCAFile] = "/certs/ca.crt"
				pod.Annotations[annotationPrometheusCAPath] = "/certs/ca/"
				pod.Annotations[annotationPrometheusCertFile] = "/certs/server.crt"
				pod.Annotations[annotationPrometheusKeyFile] = "/certs/key.pem"
				return pod
			},
			MeshWebhook{
				ConsulAPITimeout: 5 * time.Second,
			},
			`# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -prometheus-scrape-path="/scrape-path" \
  -prometheus-backend-port="20100" \
  -prometheus-ca-file="/certs/ca.crt" \
  -prometheus-ca-path="/certs/ca/" \
  -prometheus-cert-file="/certs/server.crt" \
  -prometheus-key-file="/certs/key.pem" \
  -bootstrap > /consul/connect-inject/envoy-bootstrap.yaml`,
			"",
			"",
		},
		{
			"When providing Prometheus TLS config, missing CA gives an error",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				pod.Annotations[annotationEnableMetrics] = "true"
				pod.Annotations[annotationEnableMetricsMerging] = "true"
				pod.Annotations[annotationMergedMetricsPort] = "20100"
				pod.Annotations[annotationPrometheusScrapePort] = "22222"
				pod.Annotations[annotationPrometheusScrapePath] = "/scrape-path"
				pod.Annotations[annotationPrometheusCertFile] = "/certs/server.crt"
				pod.Annotations[annotationPrometheusKeyFile] = "/certs/key.pem"
				return pod
			},
			MeshWebhook{
				ConsulAPITimeout: 5 * time.Second,
			},
			"",
			"",
			fmt.Sprintf("Must set one of %q or %q", annotationPrometheusCAFile, annotationPrometheusCAPath),
		},
		{
			"When providing Prometheus TLS config, missing cert gives an error",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				pod.Annotations[annotationEnableMetrics] = "true"
				pod.Annotations[annotationEnableMetricsMerging] = "true"
				pod.Annotations[annotationMergedMetricsPort] = "20100"
				pod.Annotations[annotationPrometheusScrapePort] = "22222"
				pod.Annotations[annotationPrometheusScrapePath] = "/scrape-path"
				pod.Annotations[annotationPrometheusCAFile] = "/certs/ca.crt"
				pod.Annotations[annotationPrometheusKeyFile] = "/certs/key.pem"
				return pod
			},
			MeshWebhook{
				ConsulAPITimeout: 5 * time.Second,
			},
			"",
			"",
			fmt.Sprintf("Must set %q", annotationPrometheusCertFile),
		},
		{
			"When providing Prometheus TLS config, missing key gives an error",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				pod.Annotations[annotationEnableMetrics] = "true"
				pod.Annotations[annotationEnableMetricsMerging] = "true"
				pod.Annotations[annotationMergedMetricsPort] = "20100"
				pod.Annotations[annotationPrometheusScrapePort] = "22222"
				pod.Annotations[annotationPrometheusScrapePath] = "/scrape-path"
				pod.Annotations[annotationPrometheusCAPath] = "/certs/ca/"
				pod.Annotations[annotationPrometheusCertFile] = "/certs/server.crt"
				return pod
			},
			MeshWebhook{
				ConsulAPITimeout: 5 * time.Second,
			},
			"",
			"",
			fmt.Sprintf("Must set %q", annotationPrometheusKeyFile),
		},
	}

	for _, tt := range cases {
		t.Run(tt.Name, func(t *testing.T) {
			require := require.New(t)

			h := tt.Webhook
			pod := *tt.Pod(minimal())
			container, err := h.containerInit(testNS, pod, multiPortInfo{})
			if tt.ErrStr == "" {
				require.NoError(err)
			} else {
				require.Contains(err.Error(), tt.ErrStr)
			}
			actual := strings.Join(container.Command, " ")
			require.Contains(actual, tt.Cmd)
			if tt.CmdNot != "" {
				require.NotContains(actual, tt.CmdNot)
			}
		})
	}
}

func TestHandlerContainerInit_transparentProxy(t *testing.T) {
	cases := map[string]struct {
		globalEnabled          bool
		cniEnabled             bool
		annotations            map[string]string
		expectedContainsCmd    string
		expectedNotContainsCmd string
		namespaceLabel         map[string]string
	}{
		"enabled globally, ns not set, annotation not provided, cni disabled": {
			true,
			false,
			nil,
			`/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			"",
			nil,
		},
		"enabled globally, ns not set, annotation is false, cni disabled": {
			true,
			false,
			map[string]string{keyTransparentProxy: "false"},
			"",
			`/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			nil,
		},
		"enabled globally, ns not set, annotation is true, cni disabled": {
			true,
			false,
			map[string]string{keyTransparentProxy: "true"},
			`/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			"",
			nil,
		},
		"disabled globally, ns not set, annotation not provided, cni disabled": {
			false,
			false,
			nil,
			"",
			`/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			nil,
		},
		"disabled globally, ns not set, annotation is false, cni disabled": {
			false,
			false,
			map[string]string{keyTransparentProxy: "false"},
			"",
			`/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			nil,
		},
		"disabled globally, ns not set, annotation is true, cni disabled": {
			false,
			false,
			map[string]string{keyTransparentProxy: "true"},
			`/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			"",
			nil,
		},
		"exclude-inbound-ports, ns is not set, annotation is provided, cni disabled": {
			true,
			false,
			map[string]string{
				keyTransparentProxy:                 "true",
				annotationTProxyExcludeInboundPorts: "9090,9091",
			},
			`/consul/connect-inject/consul connect redirect-traffic \
  -exclude-inbound-port="9090" \
  -exclude-inbound-port="9091" \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			"",
			nil,
		},
		"exclude-outbound-ports, ns is not set, annotation is provided, cni disabled": {
			true,
			false,
			map[string]string{
				keyTransparentProxy:                  "true",
				annotationTProxyExcludeOutboundPorts: "9090,9091",
			},
			`/consul/connect-inject/consul connect redirect-traffic \
  -exclude-outbound-port="9090" \
  -exclude-outbound-port="9091" \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			"",
			nil,
		},
		"exclude-outbound-cidrs annotation is provided, cni disabled": {
			true,
			false,
			map[string]string{
				keyTransparentProxy:                  "true",
				annotationTProxyExcludeOutboundCIDRs: "1.1.1.1,2.2.2.2/24",
			},
			`/consul/connect-inject/consul connect redirect-traffic \
  -exclude-outbound-cidr="1.1.1.1" \
  -exclude-outbound-cidr="2.2.2.2/24" \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			"",
			nil,
		},
		"exclude-uids annotation is provided, ns is not set, cni disabled": {
			true,
			false,
			map[string]string{
				keyTransparentProxy:         "true",
				annotationTProxyExcludeUIDs: "6000,7000",
			},
			`/consul/connect-inject/consul connect redirect-traffic \
  -exclude-uid="6000" \
  -exclude-uid="7000" \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			"",
			nil,
		},
		"disabled globally, ns enabled, annotation not set, cni disabled": {
			false,
			false,
			nil,
			`/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			"",
			map[string]string{keyTransparentProxy: "true"},
		},
		"enabled globally, ns disabled, annotation not set, cni disabled": {
			true,
			false,
			nil,
			"",
			`/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			map[string]string{keyTransparentProxy: "false"},
		},
		"disabled globally, ns enabled, annotation not set, cni enabled": {
			false,
			true,
			nil,
			"",
			`/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			map[string]string{keyTransparentProxy: "true"},
		},

		"enabled globally, ns not set, annotation not set, cni enabled": {
			true,
			true,
			nil,
			"",
			`/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			nil,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := MeshWebhook{
				EnableTransparentProxy: c.globalEnabled,
				ConsulAPITimeout:       5 * time.Second,
				EnableCNI:              c.cniEnabled,
			}
			pod := minimal()
			pod.Annotations = c.annotations

			expectedSecurityContext := &corev1.SecurityContext{}
			if !c.cniEnabled {
				expectedSecurityContext.RunAsUser = pointer.Int64(0)
				expectedSecurityContext.RunAsGroup = pointer.Int64(0)
				expectedSecurityContext.RunAsNonRoot = pointer.Bool(false)
				expectedSecurityContext.Privileged = pointer.Bool(true)
				expectedSecurityContext.Capabilities = &corev1.Capabilities{
					Add: []corev1.Capability{netAdminCapability},
				}
			} else {

				expectedSecurityContext.RunAsUser = pointer.Int64(initContainersUserAndGroupID)
				expectedSecurityContext.RunAsGroup = pointer.Int64(initContainersUserAndGroupID)
				expectedSecurityContext.RunAsNonRoot = pointer.Bool(true)
				expectedSecurityContext.Privileged = pointer.Bool(false)
				expectedSecurityContext.Capabilities = &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				}
			}
			ns := testNS
			ns.Labels = c.namespaceLabel
			container, err := w.containerInit(ns, *pod, multiPortInfo{})
			require.NoError(t, err)
			actualCmd := strings.Join(container.Command, " ")

			if c.expectedContainsCmd != "" {
				require.Equal(t, expectedSecurityContext, container.SecurityContext)
				require.Contains(t, actualCmd, c.expectedContainsCmd)
			} else {
				if !c.cniEnabled {
					require.Nil(t, container.SecurityContext)
				} else {
					require.Equal(t, expectedSecurityContext, container.SecurityContext)
				}
				require.NotContains(t, actualCmd, c.expectedNotContainsCmd)
			}
		})
	}
}

func TestHandlerContainerInit_consulDNS(t *testing.T) {
	cases := map[string]struct {
		globalEnabled       bool
		annotations         map[string]string
		expectedContainsCmd string
		namespaceLabel      map[string]string
	}{
		"enabled globally, ns not set, annotation not provided": {
			globalEnabled: true,
			expectedContainsCmd: `/consul/connect-inject/consul connect redirect-traffic \
  -consul-dns-ip="10.0.34.16" \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
		},
		"enabled globally, ns not set, annotation is false": {
			globalEnabled: true,
			annotations:   map[string]string{keyConsulDNS: "false"},
			expectedContainsCmd: `/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
		},
		"enabled globally, ns not set, annotation is true": {
			globalEnabled: true,
			annotations:   map[string]string{keyConsulDNS: "true"},
			expectedContainsCmd: `/consul/connect-inject/consul connect redirect-traffic \
  -consul-dns-ip="10.0.34.16" \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
		},
		"disabled globally, ns not set, annotation not provided": {
			expectedContainsCmd: `/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
		},
		"disabled globally, ns not set, annotation is false": {
			annotations: map[string]string{keyConsulDNS: "false"},
			expectedContainsCmd: `/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
		},
		"disabled globally, ns not set, annotation is true": {
			annotations: map[string]string{keyConsulDNS: "true"},
			expectedContainsCmd: `/consul/connect-inject/consul connect redirect-traffic \
  -consul-dns-ip="10.0.34.16" \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
		},
		"disabled globally, ns enabled, annotation not set": {
			expectedContainsCmd: `/consul/connect-inject/consul connect redirect-traffic \
  -consul-dns-ip="10.0.34.16" \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			namespaceLabel: map[string]string{keyConsulDNS: "true"},
		},
		"enabled globally, ns disabled, annotation not set": {
			globalEnabled: true,
			expectedContainsCmd: `/consul/connect-inject/consul connect redirect-traffic \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
			namespaceLabel: map[string]string{keyConsulDNS: "false"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := MeshWebhook{
				EnableConsulDNS:        c.globalEnabled,
				EnableTransparentProxy: true,
				ResourcePrefix:         "consul-consul",
				ConsulAPITimeout:       5 * time.Second,
			}
			os.Setenv("CONSUL_CONSUL_DNS_SERVICE_HOST", "10.0.34.16")
			defer os.Unsetenv("CONSUL_CONSUL_DNS_SERVICE_HOST")

			pod := minimal()
			pod.Annotations = c.annotations

			ns := testNS
			ns.Labels = c.namespaceLabel
			container, err := w.containerInit(ns, *pod, multiPortInfo{})
			require.NoError(t, err)
			actualCmd := strings.Join(container.Command, " ")

			require.Contains(t, actualCmd, c.expectedContainsCmd)
		})
	}
}

func TestHandler_constructDNSServiceHostName(t *testing.T) {
	cases := []struct {
		prefix string
		result string
	}{
		{
			prefix: "consul-consul",
			result: "CONSUL_CONSUL_DNS_SERVICE_HOST",
		},
		{
			prefix: "release",
			result: "RELEASE_DNS_SERVICE_HOST",
		},
		{
			prefix: "consul-dc1",
			result: "CONSUL_DC1_DNS_SERVICE_HOST",
		},
	}

	for _, c := range cases {
		t.Run(c.prefix, func(t *testing.T) {
			w := MeshWebhook{ResourcePrefix: c.prefix, ConsulAPITimeout: 5 * time.Second}
			require.Equal(t, c.result, w.constructDNSServiceHostName())
		})
	}
}

func TestHandlerContainerInit_namespacesAndPartitionsEnabled(t *testing.T) {
	minimal := func() *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					annotationService: "foo",
				},
			},

			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "web",
					},
					{
						Name: "web-side",
					},
					{
						Name: "auth-method-secret",
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "service-account-secret",
								MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
							},
						},
					},
				},
				ServiceAccountName: "web",
			},
		}
	}

	cases := []struct {
		Name    string
		Pod     func(*corev1.Pod) *corev1.Pod
		Webhook MeshWebhook
		Cmd     string // Strings.Contains test
	}{
		{
			"whole template, default namespace, no partition",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				return pod
			},
			MeshWebhook{
				EnableNamespaces:           true,
				ConsulDestinationNamespace: "default",
				ConsulPartition:            "",
				ConsulAPITimeout:           5 * time.Second,
			},
			`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -consul-service-namespace="default" \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -namespace="default" \
  -bootstrap > /consul/connect-inject/envoy-bootstrap.yaml`,
		},
		{
			"whole template, default namespace, default partition",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				return pod
			},
			MeshWebhook{
				EnableNamespaces:           true,
				ConsulDestinationNamespace: "default",
				ConsulPartition:            "default",
				ConsulAPITimeout:           5 * time.Second,
			},
			`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -partition="default" \
  -consul-service-namespace="default" \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -partition="default" \
  -namespace="default" \
  -bootstrap > /consul/connect-inject/envoy-bootstrap.yaml`,
		},
		{
			"whole template, non-default namespace, no partition",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				return pod
			},
			MeshWebhook{
				EnableNamespaces:           true,
				ConsulDestinationNamespace: "non-default",
				ConsulPartition:            "",
				ConsulAPITimeout:           5 * time.Second,
			},
			`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -consul-service-namespace="non-default" \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -namespace="non-default" \
  -bootstrap > /consul/connect-inject/envoy-bootstrap.yaml`,
		},
		{
			"whole template, non-default namespace, non-default partition",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				return pod
			},
			MeshWebhook{
				EnableNamespaces:           true,
				ConsulDestinationNamespace: "non-default",
				ConsulPartition:            "non-default-part",
				ConsulAPITimeout:           5 * time.Second,
			},
			`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -partition="non-default-part" \
  -consul-service-namespace="non-default" \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -partition="non-default-part" \
  -namespace="non-default" \
  -bootstrap > /consul/connect-inject/envoy-bootstrap.yaml`,
		},
		{
			"Whole template, auth method, non-default namespace, mirroring disabled, default partition",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = ""
				return pod
			},
			MeshWebhook{
				AuthMethod:                 "auth-method",
				EnableNamespaces:           true,
				ConsulDestinationNamespace: "non-default",
				ConsulPartition:            "default",
				ConsulAPITimeout:           5 * time.Second,
			},
			`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -acl-auth-method="auth-method" \
  -service-account-name="web" \
  -service-name="" \
  -bearer-token-file=/var/run/secrets/kubernetes.io/serviceaccount/token \
  -auth-method-namespace="non-default" \
  -partition="default" \
  -consul-service-namespace="non-default" \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -token-file="/consul/connect-inject/acl-token" \
  -partition="default" \
  -namespace="non-default" \
  -bootstrap > /consul/connect-inject/envoy-bootstrap.yaml`,
		},
		{
			"Whole template, auth method, non-default namespace, mirroring enabled, non-default partition",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = ""
				return pod
			},
			MeshWebhook{
				AuthMethod:                 "auth-method",
				EnableNamespaces:           true,
				ConsulDestinationNamespace: "non-default", // Overridden by mirroring
				EnableK8SNSMirroring:       true,
				ConsulPartition:            "non-default",
				ConsulAPITimeout:           5 * time.Second,
			},
			`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -acl-auth-method="auth-method" \
  -service-account-name="web" \
  -service-name="" \
  -bearer-token-file=/var/run/secrets/kubernetes.io/serviceaccount/token \
  -auth-method-namespace="default" \
  -partition="non-default" \
  -consul-service-namespace="k8snamespace" \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -token-file="/consul/connect-inject/acl-token" \
  -partition="non-default" \
  -namespace="k8snamespace" \
  -bootstrap > /consul/connect-inject/envoy-bootstrap.yaml`,
		},
		{
			"whole template, default namespace, tproxy enabled, no partition",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				return pod
			},
			MeshWebhook{
				EnableNamespaces:           true,
				ConsulDestinationNamespace: "default",
				ConsulPartition:            "",
				EnableTransparentProxy:     true,
				ConsulAPITimeout:           5 * time.Second,
			},
			`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -consul-service-namespace="default" \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -namespace="default" \
  -bootstrap > /consul/connect-inject/envoy-bootstrap.yaml

# Apply traffic redirection rules.
/consul/connect-inject/consul connect redirect-traffic \
  -namespace="default" \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
		},
		{
			"whole template, non-default namespace, tproxy enabled, default partition",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				return pod
			},
			MeshWebhook{
				EnableNamespaces:           true,
				ConsulPartition:            "default",
				ConsulDestinationNamespace: "non-default",
				EnableTransparentProxy:     true,
				ConsulAPITimeout:           5 * time.Second,
			},
			`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -partition="default" \
  -consul-service-namespace="non-default" \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -partition="default" \
  -namespace="non-default" \
  -bootstrap > /consul/connect-inject/envoy-bootstrap.yaml

# Apply traffic redirection rules.
/consul/connect-inject/consul connect redirect-traffic \
  -partition="default" \
  -namespace="non-default" \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
		},

		{
			"Whole template, auth method, non-default namespace, mirroring enabled, tproxy enabled, non-default partition",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				return pod
			},
			MeshWebhook{
				AuthMethod:                 "auth-method",
				EnableNamespaces:           true,
				ConsulPartition:            "non-default",
				ConsulDestinationNamespace: "non-default", // Overridden by mirroring
				EnableK8SNSMirroring:       true,
				EnableTransparentProxy:     true,
				ConsulAPITimeout:           5 * time.Second,
			},
			`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -acl-auth-method="auth-method" \
  -service-account-name="web" \
  -service-name="web" \
  -bearer-token-file=/var/run/secrets/kubernetes.io/serviceaccount/token \
  -auth-method-namespace="default" \
  -partition="non-default" \
  -consul-service-namespace="k8snamespace" \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -token-file="/consul/connect-inject/acl-token" \
  -partition="non-default" \
  -namespace="k8snamespace" \
  -bootstrap > /consul/connect-inject/envoy-bootstrap.yaml

# Apply traffic redirection rules.
/consul/connect-inject/consul connect redirect-traffic \
  -token-file="/consul/connect-inject/acl-token" \
  -partition="non-default" \
  -namespace="k8snamespace" \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -proxy-uid=5995`,
		},
	}

	for _, tt := range cases {
		t.Run(tt.Name, func(t *testing.T) {
			require := require.New(t)

			w := tt.Webhook
			container, err := w.containerInit(testNS, *tt.Pod(minimal()), multiPortInfo{})
			require.NoError(err)
			actual := strings.Join(container.Command, " ")
			require.Equal(tt.Cmd, actual)
		})
	}
}

func TestHandlerContainerInit_Multiport(t *testing.T) {
	minimal := func() *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					annotationService: "web,web-admin",
				},
			},

			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "web-admin-service-account",
					},
				},
				Containers: []corev1.Container{
					{
						Name: "web",
					},
					{
						Name: "web-side",
					},
					{
						Name: "web-admin",
					},
					{
						Name: "web-admin-side",
					},
					{
						Name: "auth-method-secret",
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "service-account-secret",
								MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
							},
						},
					},
				},
				ServiceAccountName: "web",
			},
		}
	}

	cases := []struct {
		Name              string
		Pod               func(*corev1.Pod) *corev1.Pod
		Webhook           MeshWebhook
		NumInitContainers int
		MultiPortInfos    []multiPortInfo
		Cmd               []string // Strings.Contains test
	}{
		{
			"Whole template, multiport",
			func(pod *corev1.Pod) *corev1.Pod {
				return pod
			},
			MeshWebhook{ConsulAPITimeout: 5 * time.Second},
			2,
			[]multiPortInfo{
				{
					serviceIndex: 0,
					serviceName:  "web",
				},
				{
					serviceIndex: 1,
					serviceName:  "web-admin",
				},
			},
			[]string{
				`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -multiport=true \
  -proxy-id-file=/consul/connect-inject/proxyid-web \
  -service-name="web" \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid-web)" \
  -admin-bind=127.0.0.1:19000 \
  -bootstrap > /consul/connect-inject/envoy-bootstrap-web.yaml`,

				`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -multiport=true \
  -proxy-id-file=/consul/connect-inject/proxyid-web-admin \
  -service-name="web-admin" \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid-web-admin)" \
  -admin-bind=127.0.0.1:19001 \
  -bootstrap > /consul/connect-inject/envoy-bootstrap-web-admin.yaml`,
			},
		},
		{
			"Whole template, multiport, auth method",
			func(pod *corev1.Pod) *corev1.Pod {
				return pod
			},
			MeshWebhook{
				AuthMethod:       "auth-method",
				ConsulAPITimeout: 5 * time.Second,
			},
			2,
			[]multiPortInfo{
				{
					serviceIndex: 0,
					serviceName:  "web",
				},
				{
					serviceIndex: 1,
					serviceName:  "web-admin",
				},
			},
			[]string{
				`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -acl-auth-method="auth-method" \
  -service-account-name="web" \
  -service-name="web" \
  -bearer-token-file=/var/run/secrets/kubernetes.io/serviceaccount/token \
  -acl-token-sink=/consul/connect-inject/acl-token-web \
  -multiport=true \
  -proxy-id-file=/consul/connect-inject/proxyid-web \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid-web)" \
  -token-file="/consul/connect-inject/acl-token-web" \
  -admin-bind=127.0.0.1:19000 \
  -bootstrap > /consul/connect-inject/envoy-bootstrap-web.yaml`,

				`/bin/sh -ec 
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -acl-auth-method="auth-method" \
  -service-account-name="web-admin" \
  -service-name="web-admin" \
  -bearer-token-file=/consul/serviceaccount-web-admin/token \
  -acl-token-sink=/consul/connect-inject/acl-token-web-admin \
  -multiport=true \
  -proxy-id-file=/consul/connect-inject/proxyid-web-admin \

# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid-web-admin)" \
  -token-file="/consul/connect-inject/acl-token-web-admin" \
  -admin-bind=127.0.0.1:19001 \
  -bootstrap > /consul/connect-inject/envoy-bootstrap-web-admin.yaml`,
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.Name, func(t *testing.T) {
			require := require.New(t)

			w := tt.Webhook
			for i := 0; i < tt.NumInitContainers; i++ {
				container, err := w.containerInit(testNS, *tt.Pod(minimal()), tt.MultiPortInfos[i])
				require.NoError(err)
				actual := strings.Join(container.Command, " ")
				require.Equal(tt.Cmd[i], actual)
			}
		})
	}
}

func TestHandlerContainerInit_authMethod(t *testing.T) {
	require := require.New(t)
	w := MeshWebhook{
		AuthMethod:       "release-name-consul-k8s-auth-method",
		ConsulAPITimeout: 5 * time.Second,
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				annotationService: "foo",
			},
		},

		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "web",
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "default-token-podid",
							ReadOnly:  true,
							MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
						},
					},
				},
			},
			ServiceAccountName: "foo",
		},
	}
	container, err := w.containerInit(testNS, *pod, multiPortInfo{})
	require.NoError(err)
	actual := strings.Join(container.Command, " ")
	require.Contains(actual, `
consul-k8s-control-plane connect-init -pod-name=${POD_NAME} -pod-namespace=${POD_NAMESPACE} \
  -consul-api-timeout=5s \
  -acl-auth-method="release-name-consul-k8s-auth-method"`)
	require.Contains(actual, `
# Generate the envoy bootstrap code
/consul/connect-inject/consul connect envoy \
  -proxy-id="$(cat /consul/connect-inject/proxyid)" \
  -token-file="/consul/connect-inject/acl-token" \
  -bootstrap > /consul/connect-inject/envoy-bootstrap.yaml`)
}

// If Consul CA cert is set,
// Consul addresses should use HTTPS
// and CA cert should be set as env variable.
func TestHandlerContainerInit_WithTLS(t *testing.T) {
	require := require.New(t)
	w := MeshWebhook{
		ConsulCACert:     "consul-ca-cert",
		ConsulAPITimeout: 5 * time.Second,
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				annotationService: "foo",
			},
		},

		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "web",
				},
			},
		},
	}
	container, err := w.containerInit(testNS, *pod, multiPortInfo{})
	require.NoError(err)
	actual := strings.Join(container.Command, " ")
	require.Contains(actual, `
export CONSUL_HTTP_ADDR="https://${HOST_IP}:8501"
export CONSUL_GRPC_ADDR="https://${HOST_IP}:8502"
export CONSUL_CACERT=/consul/connect-inject/consul-ca.pem
cat <<EOF >/consul/connect-inject/consul-ca.pem
consul-ca-cert
EOF`)
	require.NotContains(actual, `
export CONSUL_HTTP_ADDR="${HOST_IP}:8500"
export CONSUL_GRPC_ADDR="${HOST_IP}:8502"`)
}

func TestHandlerContainerInit_Resources(t *testing.T) {
	require := require.New(t)
	w := MeshWebhook{
		InitContainerResources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("10Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("20m"),
				corev1.ResourceMemory: resource.MustParse("25Mi"),
			},
		},
		ConsulAPITimeout: 5 * time.Second,
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				annotationService: "foo",
			},
		},

		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "web",
				},
			},
		},
	}
	container, err := w.containerInit(testNS, *pod, multiPortInfo{})
	require.NoError(err)
	require.Equal(corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("20m"),
			corev1.ResourceMemory: resource.MustParse("25Mi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("10m"),
			corev1.ResourceMemory: resource.MustParse("10Mi"),
		},
	}, container.Resources)
}

// Test that the init copy container has the correct command and SecurityContext.
func TestHandlerInitCopyContainer(t *testing.T) {
	openShiftEnabledCases := []bool{false, true}

	for _, openShiftEnabled := range openShiftEnabledCases {
		t.Run(fmt.Sprintf("openshift enabled: %t", openShiftEnabled), func(t *testing.T) {
			w := MeshWebhook{EnableOpenShift: openShiftEnabled, ConsulAPITimeout: 5 * time.Second}

			container := w.initCopyContainer()

			if openShiftEnabled {
				require.Nil(t, container.SecurityContext)
			} else {
				expectedSecurityContext := &corev1.SecurityContext{
					RunAsUser:              pointer.Int64(initContainersUserAndGroupID),
					RunAsGroup:             pointer.Int64(initContainersUserAndGroupID),
					RunAsNonRoot:           pointer.Bool(true),
					ReadOnlyRootFilesystem: pointer.Bool(true),
				}
				require.Equal(t, expectedSecurityContext, container.SecurityContext)
			}

			actual := strings.Join(container.Command, " ")
			require.Contains(t, actual, `cp /bin/consul /consul/connect-inject/consul`)
		})
	}
}

var testNS = corev1.Namespace{
	ObjectMeta: metav1.ObjectMeta{
		Name: k8sNamespace,
	},
}
