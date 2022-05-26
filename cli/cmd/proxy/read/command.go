package read

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/hashicorp/consul-k8s/cli/common"
	"github.com/hashicorp/consul-k8s/cli/common/flag"
	"github.com/hashicorp/consul-k8s/cli/common/terminal"
	helmCLI "helm.sh/helm/v3/pkg/cli"
	"k8s.io/client-go/kubernetes"
)

var (
	kubecontext = "teckert@hashicorp.com@thomas-eks-test.us-east-2.eksctl.io"
)

func defaultKubeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home + "/.kube/config", nil
}

type Command struct {
	*common.BaseCommand

	kubernetes kubernetes.Interface

	set *flag.Sets

	flagPodName   string
	flagNamespace string
	flagJSON      bool

	flagKubeConfig  string
	flagKubeContext string

	once sync.Once
	help string
}

func (c *Command) init() {
	kubeconfig, err := defaultKubeConfigPath()
	if err != nil {
		panic(err)
	}

	c.set = flag.NewSets()
	f := c.set.NewSet("Command Options")
	f.StringVar(&flag.StringVar{
		Name:    "pod",
		Aliases: []string{"p"},
		Target:  &c.flagPodName,
	})
	f.StringVar(&flag.StringVar{
		Name:    "namespace",
		Target:  &c.flagNamespace,
		Default: "default",
		Usage:   "The namespace to list proxies in.",
		Aliases: []string{"n"},
	})
	f.BoolVar(&flag.BoolVar{
		Name:    "json",
		Target:  &c.flagJSON,
		Default: false,
		Usage:   "Output the whole Envoy Config as JSON.",
	})

	f = c.set.NewSet("GlobalOptions")
	f.StringVar(&flag.StringVar{
		Name:    "kubeconfig",
		Aliases: []string{"c"},
		Target:  &c.flagKubeConfig,
		Default: kubeconfig,
		Usage:   "Set the path to kubeconfig file.",
	})
	f.StringVar(&flag.StringVar{
		Name:    "context",
		Target:  &c.flagKubeContext,
		Default: kubecontext,
		Usage:   "Set the Kubernetes context to use.",
	})

	c.help = c.set.Help()

	c.Init()
}

func (c *Command) Run(args []string) int {
	c.once.Do(c.init)
	c.Log.ResetNamed("read")
	defer common.CloseWithError(c.BaseCommand)

	if err := c.set.Parse(args); err != nil {
		c.UI.Output(err.Error())
		return 1
	}

	if c.flagPodName == "" {
		c.UI.Output(c.help)
		return 1
	}

	settings := helmCLI.New()
	if c.flagKubeConfig != "" {
		settings.KubeConfig = c.flagKubeConfig
	}
	if c.flagKubeContext != "" {
		settings.KubeContext = c.flagKubeContext
	}
	if c.kubernetes == nil {
		restConfig, err := settings.RESTClientGetter().ToRESTConfig()
		if err != nil {
			c.UI.Output("Error retrieving Kubernetes authentication:\n%v", err, terminal.WithErrorStyle())
			return 1
		}
		c.kubernetes, err = kubernetes.NewForConfig(restConfig)
		if err != nil {
			c.UI.Output("Error initializing Kubernetes client:\n%v", err, terminal.WithErrorStyle())
			return 1
		}
	}

	pf := common.PortForward{
		Namespace:   c.flagNamespace,
		PodName:     c.flagPodName,
		RemotePort:  19000,
		KubeClient:  c.kubernetes,
		KubeConfig:  settings.KubeConfig,
		KubeContext: settings.KubeContext,
	}
	if err := pf.Open(); err != nil {
		c.UI.Output("Error opening port forward:\n%v", err, terminal.WithErrorStyle())
		return 1
	}
	defer pf.Close()

	endpoint, err := pf.Endpoint()
	if err != nil {
		c.UI.Output("Error getting endpoint:\n%v", err, terminal.WithErrorStyle())
		return 1
	}

	response, err := http.Get(fmt.Sprintf("%s/config_dump?include_eds", endpoint))
	if err != nil {
		c.UI.Output("Error getting config dump:\n%v", err, terminal.WithErrorStyle())
		return 1
	}
	defer response.Body.Close()

	config, err := io.ReadAll(response.Body)
	if err != nil {
		c.UI.Output("Error reading config dump:\n%v", err, terminal.WithErrorStyle())
		return 1
	}

	c.UI.Output(fmt.Sprintf("%s Proxy Configuration", c.flagPodName), terminal.WithHeaderStyle())
	if c.flagJSON {
		c.UI.Output(string(config))
	} else {
		err = c.Print(config)
		if err != nil {
			c.UI.Output("Error printing config:\n%v", err, terminal.WithErrorStyle())
			return 1
		}
	}

	return 0
}

func (c *Command) Synopsis() string {
	return ""
}

func (c *Command) Help() string {
	return ""
}

func (c *Command) Print(config []byte) error {
	var envCfg map[string]interface{}

	json.Unmarshal(config, &envCfg)

	for _, cfg := range envCfg["configs"].([]interface{}) {
		a := cfg.(map[string]interface{})
		cfgType := a["@type"].(string)

		switch cfgType {
		case "type.googleapis.com/envoy.admin.v3.ClustersConfigDump":
			err := PrintClusters(c.UI, a)
			if err != nil {
				return err
			}
		case "type.googleapis.com/envoy.admin.v3.EndpointsConfigDump":
			err := c.PrintEndpoints(a)
			if err != nil {
				return err
			}
		case "type.googleapis.com/envoy.admin.v3.ListenersConfigDump":
			err := c.PrintListeners(a)
			if err != nil {
				return err
			}
		case "type.googleapis.com/envoy.admin.v3.RoutesConfigDump":
			err := c.PrintRoutes(a)
			if err != nil {
				return err
			}
		case "type.googleapis.com/envoy.admin.v3.SecretsConfigDump":
			err := c.PrintSecrets(a)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (c *Command) PrintListeners(listeners map[string]interface{}) error {
	c.UI.Output("\nListeners:")

	tbl := terminal.NewTable("Name", "Address:Port", "Direction", "Filter Chain Match", "Destination Cluster", "Last Updated")

	if listeners["dynamic_listeners"] != nil {

		for _, listener := range listeners["dynamic_listeners"].([]interface{}) {
			l := listener.(map[string]interface{})
			al := l["active_state"].(map[string]interface{})
			name := strings.Split(l["name"].(string), ":")[0]
			addr := strings.SplitN(l["name"].(string), ":", 2)[1]
			l2 := al["listener"].(map[string]interface{})
			direction := l2["traffic_direction"].(string)
			fcm := []string{}
			dest := []string{}
			lastUpdated := al["last_updated"].(string)

			if direction == "OUTBOUND" {
				fcs := l2["filter_chains"].([]interface{})
				for _, fc := range fcs {
					fc_ := fc.(map[string]interface{})
					if fc_["filter_chain_match"] != nil {
						fcmtch := fc_["filter_chain_match"].(map[string]interface{})
						prs := fcmtch["prefix_ranges"].([]interface{})
						for _, pr := range prs {
							pr_ := pr.(map[string]interface{})
							fcm = append(fcm, pr_["address_prefix"].(string))
						}
					}
					if fc_["filters"] != nil {
						fltrs := fc_["filters"].([]interface{})
						for _, fltr := range fltrs {
							fltr_ := fltr.(map[string]interface{})
							if fltr_["typed_config"] != nil {
								tc := fltr_["typed_config"].(map[string]interface{})
								if tc["cluster"] != nil {
									dest = append(dest, strings.Split(tc["cluster"].(string), ".")[0])
								}
								if tc["route_config"] != nil {
									rc := tc["route_config"].(map[string]interface{})
									if rc["virtual_hosts"] != nil {
										vhs := rc["virtual_hosts"].([]interface{})
										for _, vh := range vhs {
											vh_ := vh.(map[string]interface{})
											if vh_["routes"] != nil {
												rts := vh_["routes"].([]interface{})
												for _, rt := range rts {
													rt_ := rt.(map[string]interface{})
													r := rt_["route"].(map[string]interface{})
													dest = append(dest, strings.Split(r["cluster"].(string), ".")[0])
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
			tbl.Rich([]string{name, addr, direction, strings.Join(fcm, ", "), strings.Join(dest, ", "), lastUpdated}, []string{})
		}
	}

	c.UI.Table(tbl)
	return nil
}

func (c *Command) PrintRoutes(routes map[string]interface{}) error {
	c.UI.Output("\nRoutes:")
	tbl := terminal.NewTable("Name", "Destination Cluster", "Last Updated")
	c.UI.Table(tbl)

	return nil
}

func (c *Command) PrintEndpoints(endpoints map[string]interface{}) error {
	c.UI.Output("\nEndpoints:")
	tbl := terminal.NewTable("Endpoint", "Cluster", "Weight", "Status")

	if endpoints["static_endpoint_configs"] != nil {
		for _, endpoint := range endpoints["static_endpoint_configs"].([]interface{}) {
			e := endpoint.(map[string]interface{})
			epcfg := e["endpoint_config"].(map[string]interface{})

			cluster := epcfg["cluster_name"].(string)

			if epcfg["endpoints"] != nil {
				for _, ep := range epcfg["endpoints"].([]interface{}) {
					ep_ := ep.(map[string]interface{})
					lbendps := ep_["lb_endpoints"].([]interface{})
					for _, lbep := range lbendps {
						lbep_ := lbep.(map[string]interface{})
						e__ := lbep_["endpoint"].(map[string]interface{})
						a__ := e__["address"].(map[string]interface{})
						saddr := a__["socket_address"].(map[string]interface{})
						addr := saddr["address"].(string)
						port := saddr["port_value"].(float64)
						endp := fmt.Sprintf("%s:%d", addr, int(port))
						weight := fmt.Sprintf("%d", int(lbep_["load_balancing_weight"].(float64)))
						status := lbep_["health_status"].(string)

						tbl.Rich([]string{endp, cluster, weight, status}, []string{})
					}
				}
			}
		}
	}

	if endpoints["dynamic_endpoint_configs"] != nil {
		for _, endpoint := range endpoints["dynamic_endpoint_configs"].([]interface{}) {
			e := endpoint.(map[string]interface{})
			epcfg := e["endpoint_config"].(map[string]interface{})

			cluster := ""
			if epcfg["cluster_name"] != nil {
				cluster = epcfg["cluster_name"].(string)
			}

			if epcfg["endpoints"] != nil {
				for _, ep := range epcfg["endpoints"].([]interface{}) {
					ep_ := ep.(map[string]interface{})
					lbendps := ep_["lb_endpoints"].([]interface{})
					for _, lbep := range lbendps {
						lbep_ := lbep.(map[string]interface{})
						e__ := lbep_["endpoint"].(map[string]interface{})
						a__ := e__["address"].(map[string]interface{})
						saddr := a__["socket_address"].(map[string]interface{})
						addr := saddr["address"].(string)
						port := saddr["port_value"].(float64)
						endp := fmt.Sprintf("%s:%d", addr, int(port))
						weight := fmt.Sprintf("%d", int(lbep_["load_balancing_weight"].(float64)))
						status := lbep_["health_status"].(string)

						tbl.Rich([]string{endp, cluster, weight, status}, []string{})
					}
				}
			}
		}
	}

	c.UI.Table(tbl)

	return nil
}

func (c *Command) PrintSecrets(secrets map[string]interface{}) error {
	c.UI.Output("\nSecrets:")

	tbl := terminal.NewTable("Name", "Type", "Status", "Valid", "Valid from", "Valid to")
	tbl.Rich([]string{"default", "Certificate chain", "Active", "True", "2022-05-24T17:41:41.997Z", "2022-09-24T17:41:41.997Z"}, []string{})
	tbl.Rich([]string{"ROOTCA", "Certificate authority", "Active", "True", "2022-05-24T18:27:29.585Z", "2022-09-24T17:41:41.997Z"}, []string{})

	c.UI.Table(tbl)
	return nil
}
