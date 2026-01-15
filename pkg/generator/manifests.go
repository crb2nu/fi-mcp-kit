package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	"gopkg.in/yaml.v3"
)

const k8sSharedSecretsName = "mcp-secrets"

type GatewayManifests struct {
	Enabled bool

	Name  string
	Image string

	Replicas int

	ServicePort int

	// IngressHost controls whether an Ingress is generated. If empty, ingress is skipped.
	IngressHost string

	IngressClassName string
	TLSSecretName    string
}

type ManifestsOptions struct {
	Namespace     string
	ImageRegistry string
	Gateway       GatewayManifests
}

// GenerateManifests generates Kubernetes manifests for the MCP Hub.
func GenerateManifests(reg *registry.Registry, outputDir string, opts ManifestsOptions) error {
	namespace := opts.Namespace
	imageRegistry := opts.ImageRegistry
	if namespace == "" {
		namespace = "mcp-hub"
	}
	if imageRegistry == "" {
		imageRegistry = "registry.harbor.lan/mcp"
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	var generatedResources []string

	for _, server := range reg.Servers {
		if server.IsLocalOnly() {
			continue
		}

		k8sName := sanitizeName(server.Name)
		serverDir := filepath.Join(outputDir, k8sName)
		if err := os.MkdirAll(serverDir, 0755); err != nil {
			return fmt.Errorf("create server dir: %w", err)
		}

		deploy, err := createDeployment(server, namespace, imageRegistry)
		if err != nil {
			return fmt.Errorf("create deployment %s: %w", server.Name, err)
		}
		if err := writeYaml(filepath.Join(serverDir, "deployment.yaml"), deploy); err != nil {
			return err
		}

		svc := createService(server, namespace)
		if err := writeYaml(filepath.Join(serverDir, "service.yaml"), svc); err != nil {
			return err
		}

		cm := createConfigMap(server, namespace)
		if cm != nil {
			if err := writeYaml(filepath.Join(serverDir, "configmap.yaml"), cm); err != nil {
				return err
			}
		}

		generatedResources = append(generatedResources, fmt.Sprintf("%s/deployment.yaml", k8sName))
		generatedResources = append(generatedResources, fmt.Sprintf("%s/service.yaml", k8sName))
		if cm != nil {
			generatedResources = append(generatedResources, fmt.Sprintf("%s/configmap.yaml", k8sName))
		}
	}

	if opts.Gateway.Enabled {
		if err := generateGatewayManifests(reg, outputDir, namespace, imageRegistry, opts.Gateway, &generatedResources); err != nil {
			return err
		}
	}

	kust := createKustomization(generatedResources, namespace)
	if err := writeYaml(filepath.Join(outputDir, "kustomization.yaml"), kust); err != nil {
		return err
	}

	return nil
}

func sanitizeName(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

func getServerType(spec *registry.TargetSpec) string {
	cmd := spec.Command
	args := spec.Args

	if cmd == "npx" || (len(args) > 0 && fmt.Sprintf("%v", args[0]) == "npx") {
		return "npx"
	}
	if cmd == "python3" || cmd == "python" || strings.HasSuffix(cmd, ".py") {
		return "python"
	}
	if cmd == "uvx" {
		return "uvx"
	}
	if strings.HasSuffix(cmd, ".sh") {
		return "shell"
	}
	return "custom"
}

type secretToken struct {
	kind       string
	key        string
	def        string
	hasDefault bool
}

func parseSecretToken(value string) (secretToken, bool) {
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return secretToken{}, false
	}

	body := strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}")
	parts := strings.SplitN(body, ":", 2)
	if len(parts) != 2 {
		return secretToken{}, false
	}

	kind := parts[0]
	if kind != "env" && kind != "keychain" && kind != "secret" {
		return secretToken{}, false
	}

	rest := parts[1]
	if rest == "" {
		return secretToken{}, false
	}

	if idx := strings.Index(rest, ":-"); idx >= 0 {
		key := rest[:idx]
		def := rest[idx+2:]
		if key == "" {
			return secretToken{}, false
		}
		return secretToken{
			kind:       kind,
			key:        key,
			def:        def,
			hasDefault: true,
		}, true
	}

	return secretToken{kind: kind, key: rest}, true
}

func k8sEnvVarFromRegistry(name string, rawValue string) map[string]any {
	// Resolve safe path placeholders, but preserve secret token patterns.
	value := ResolveTokens(rawValue, "", "cluster")

	// Convert secret tokens into Kubernetes-native secret refs.
	if tok, ok := parseSecretToken(value); ok {
		if tok.hasDefault {
			// K8s env vars don't support "secret with default". For cluster manifests,
			// materialize the default and let overlays/patches override if needed.
			return map[string]any{
				"name":  name,
				"value": ResolveTokens(tok.def, "", "cluster"),
			}
		}

		return map[string]any{
			"name": name,
			"valueFrom": map[string]any{
				"secretKeyRef": map[string]any{
					"name": k8sSharedSecretsName,
					"key":  tok.key,
				},
			},
		}
	}

	return map[string]any{
		"name":  name,
		"value": value,
	}
}

func createDeployment(server *registry.Server, namespace, imageRegistry string) (map[string]any, error) {
	k8sName := sanitizeName(server.Name)
	spec := server.Common
	if spec == nil {
		spec = &registry.TargetSpec{}
	}
	serverType := getServerType(spec)

	var image string
	switch serverType {
	case "npx":
		image = fmt.Sprintf("%s/node-server:latest", imageRegistry)
	case "python", "uvx", "shell":
		image = fmt.Sprintf("%s/python-server:latest", imageRegistry)
	default:
		image = fmt.Sprintf("%s/custom-server:latest", imageRegistry)
	}

	envVars := []map[string]any{}
	if len(spec.Env) > 0 {
		keys := make([]string, 0, len(spec.Env))
		for k := range spec.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			envVars = append(envVars, k8sEnvVarFromRegistry(k, spec.Env[k]))
		}
	}

	defaults := map[string]string{
		"MCP_SERVER_NAME": server.Name,
		"MCP_TRANSPORT":   "websocket",
		"MCP_WS_PORT":     "8080",
		"LOG_LEVEL":       "info",
	}
	existing := make(map[string]bool)
	for _, e := range envVars {
		n, _ := e["name"].(string)
		if n != "" {
			existing[n] = true
		}
	}
	for k, v := range defaults {
		if !existing[k] {
			envVars = append(envVars, map[string]any{"name": k, "value": v})
		}
	}

	var containerCmd []string
	var containerArgs []string

	if serverType == "npx" {
		containerArgs = ResolveArgs(spec.Args, "", "cluster")
	}

	if serverType != "npx" {
		cmdParts := []string{ResolveTokens(spec.Command, "", "cluster")}
		cmdParts = append(cmdParts, ResolveArgs(spec.Args, "", "cluster")...)
		fullCmd := strings.Join(cmdParts, " ")
		if fullCmd != "" {
			envVars = append(envVars, map[string]any{
				"name":  "MCP_SERVER_COMMAND",
				"value": fullCmd,
			})
		}
	}

	resources := map[string]any{
		"requests": map[string]string{"cpu": "50m", "memory": "128Mi"},
		"limits":   map[string]string{"cpu": "200m", "memory": "256Mi"},
	}
	for _, cat := range server.Categories {
		if cat == "kubernetes" || cat == "operations" {
			resources = map[string]any{
				"requests": map[string]string{"cpu": "100m", "memory": "256Mi"},
				"limits":   map[string]string{"cpu": "500m", "memory": "512Mi"},
			}
			break
		}
	}

	replicas := 1
	if serverType == "npx" || serverType == "python" || serverType == "uvx" {
		replicas = 2
	}
	if server.Name == "ops_mcp" || server.Name == "k8s_apps_k3s" {
		replicas = 2
	}

	container := map[string]any{
		"name":            k8sName,
		"image":           image,
		"imagePullPolicy": "Always",
		"env":             envVars,
		"ports":           []map[string]any{{"containerPort": 8080, "name": "websocket"}},
		"resources":       resources,
		"securityContext": map[string]any{
			"allowPrivilegeEscalation": false,
			"readOnlyRootFilesystem":   false,
			"capabilities":             map[string]any{"drop": []string{"ALL"}},
		},
		"livenessProbe": map[string]any{
			"httpGet":             map[string]any{"path": "/health", "port": 8080},
			"initialDelaySeconds": 10,
			"periodSeconds":       30,
		},
		"readinessProbe": map[string]any{
			"httpGet":             map[string]any{"path": "/ready", "port": 8080},
			"initialDelaySeconds": 5,
			"periodSeconds":       10,
		},
	}

	if len(containerCmd) > 0 {
		container["command"] = containerCmd
	}
	if len(containerArgs) > 0 {
		container["args"] = containerArgs
	}

	deploy := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      k8sName,
			"namespace": namespace,
			"labels": map[string]string{
				"app":             k8sName,
				"component":       "mcp-server",
				"mcp.server/name": server.Name,
				"mcp.server/type": serverType,
			},
		},
		"spec": map[string]any{
			"replicas": replicas,
			"selector": map[string]any{"matchLabels": map[string]string{"app": k8sName}},
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]string{
						"app":             k8sName,
						"component":       "mcp-server",
						"mcp.server/name": server.Name,
					},
					"annotations": map[string]string{
						"mcp.server/description": spec.Description,
						"mcp.server/categories":  strings.Join(server.Categories, ","),
					},
				},
				"spec": map[string]any{
					"securityContext": map[string]any{
						"runAsNonRoot": true,
						"runAsUser":    1000,
						"fsGroup":      1000,
					},
					"containers": []any{container},
				},
			},
		},
	}

	return deploy, nil
}

func createService(server *registry.Server, namespace string) map[string]any {
	k8sName := sanitizeName(server.Name)
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"name":      fmt.Sprintf("mcp-%s", k8sName),
			"namespace": namespace,
			"labels": map[string]string{
				"app":       k8sName,
				"component": "mcp-server",
			},
		},
		"spec": map[string]any{
			"type":     "ClusterIP",
			"selector": map[string]string{"app": k8sName},
			"ports": []map[string]any{
				{
					"name":       "websocket",
					"port":       8080,
					"targetPort": 8080,
					"protocol":   "TCP",
				},
			},
		},
	}
}

func createConfigMap(server *registry.Server, namespace string) map[string]any {
	k8sName := sanitizeName(server.Name)
	spec := server.Common
	if spec == nil {
		return nil
	}

	serverConfig := map[string]any{
		"name":         server.Name,
		"description":  spec.Description,
		"categories":   server.Categories,
		"timeout":      spec.Timeout,
		"always_allow": spec.AlwaysAllow,
	}

	yamlData, _ := yaml.Marshal(serverConfig)

	return map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      fmt.Sprintf("%s-config", k8sName),
			"namespace": namespace,
			"labels": map[string]string{
				"app":       k8sName,
				"component": "mcp-server",
			},
		},
		"data": map[string]string{
			"server.yaml": string(yamlData),
		},
	}
}

func createKustomization(servers []string, namespace string) map[string]any {
	resources := append([]string(nil), servers...)
	sort.Strings(resources)

	return map[string]any{
		"apiVersion": "kustomize.config.k8s.io/v1beta1",
		"kind":       "Kustomization",
		"namespace":  namespace,
		"resources":  resources,
		"commonLabels": map[string]string{
			"app.kubernetes.io/part-of":    "mcp-hub",
			"app.kubernetes.io/managed-by": "kustomize",
		},
	}
}

func writeYaml(path string, data any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	return enc.Encode(data)
}

func generateGatewayManifests(reg *registry.Registry, outputDir, namespace, imageRegistry string, gw GatewayManifests, resources *[]string) error {
	gwDir := filepath.Join(outputDir, "gateway")
	if err := os.MkdirAll(gwDir, 0755); err != nil {
		return fmt.Errorf("create gateway dir: %w", err)
	}

	if gw.Name == "" {
		gw.Name = "fi-mcp-gateway"
	}
	if gw.Replicas <= 0 {
		gw.Replicas = 2
	}
	if gw.ServicePort <= 0 {
		gw.ServicePort = 80
	}
	if gw.Image == "" {
		gw.Image = fmt.Sprintf("%s/fi-mcp-gateway:latest", imageRegistry)
	}

	// ConfigMap: embed registry.yaml so the gateway can route safely without an external mount.
	regYaml, err := yaml.Marshal(reg)
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	regCM := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      fmt.Sprintf("%s-registry", gw.Name),
			"namespace": namespace,
			"labels": map[string]string{
				"app":       gw.Name,
				"component": "mcp-gateway",
			},
		},
		"data": map[string]string{
			"registry.yaml": string(regYaml),
		},
	}
	if err := writeYaml(filepath.Join(gwDir, "registry-configmap.yaml"), regCM); err != nil {
		return err
	}
	*resources = append(*resources, "gateway/registry-configmap.yaml")

	deploy := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      gw.Name,
			"namespace": namespace,
			"labels": map[string]string{
				"app":       gw.Name,
				"component": "mcp-gateway",
			},
		},
		"spec": map[string]any{
			"replicas": gw.Replicas,
			"selector": map[string]any{"matchLabels": map[string]string{"app": gw.Name}},
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]string{
						"app":       gw.Name,
						"component": "mcp-gateway",
					},
				},
				"spec": map[string]any{
					"securityContext": map[string]any{
						"runAsNonRoot": true,
						"runAsUser":    1000,
						"fsGroup":      1000,
					},
					"volumes": []any{
						map[string]any{
							"name": "registry",
							"configMap": map[string]any{
								"name": fmt.Sprintf("%s-registry", gw.Name),
							},
						},
					},
					"containers": []any{
						map[string]any{
							"name":            gw.Name,
							"image":           gw.Image,
							"imagePullPolicy": "Always",
							"args":            []string{"--registry", "/config/registry.yaml", "--listen", ":8080"},
							"ports":           []map[string]any{{"containerPort": 8080, "name": "http"}},
							"env": []map[string]any{
								{"name": "FI_MCP_AUTH_MODE", "value": "none"},
								{"name": "FI_MCP_AUTH_REQUIRED", "value": "true"},
								{"name": "FI_MCP_POLICY_DEFAULT", "value": "allow"},
							},
							"envFrom": []map[string]any{
								{
									"secretRef": map[string]any{
										"name":     fmt.Sprintf("%s-secrets", gw.Name),
										"optional": true,
									},
								},
							},
							"volumeMounts": []map[string]any{
								{"name": "registry", "mountPath": "/config", "readOnly": true},
							},
							"resources": map[string]any{
								"requests": map[string]string{"cpu": "50m", "memory": "128Mi"},
								"limits":   map[string]string{"cpu": "500m", "memory": "512Mi"},
							},
							"livenessProbe": map[string]any{
								"httpGet":             map[string]any{"path": "/health", "port": 8080},
								"initialDelaySeconds": 10,
								"periodSeconds":       30,
							},
							"readinessProbe": map[string]any{
								"httpGet":             map[string]any{"path": "/ready", "port": 8080},
								"initialDelaySeconds": 5,
								"periodSeconds":       10,
							},
							"securityContext": map[string]any{
								"allowPrivilegeEscalation": false,
								"readOnlyRootFilesystem":   true,
								"capabilities":             map[string]any{"drop": []string{"ALL"}},
							},
						},
					},
				},
			},
		},
	}
	if err := writeYaml(filepath.Join(gwDir, "deployment.yaml"), deploy); err != nil {
		return err
	}
	*resources = append(*resources, "gateway/deployment.yaml")

	svc := map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"name":      gw.Name,
			"namespace": namespace,
			"labels": map[string]string{
				"app":       gw.Name,
				"component": "mcp-gateway",
			},
		},
		"spec": map[string]any{
			"type":     "ClusterIP",
			"selector": map[string]string{"app": gw.Name},
			"ports": []map[string]any{
				{
					"name":       "http",
					"port":       gw.ServicePort,
					"targetPort": 8080,
					"protocol":   "TCP",
				},
			},
		},
	}
	if err := writeYaml(filepath.Join(gwDir, "service.yaml"), svc); err != nil {
		return err
	}
	*resources = append(*resources, "gateway/service.yaml")

	if strings.TrimSpace(gw.IngressHost) != "" {
		ing := map[string]any{
			"apiVersion": "networking.k8s.io/v1",
			"kind":       "Ingress",
			"metadata": map[string]any{
				"name":      gw.Name,
				"namespace": namespace,
				"labels": map[string]string{
					"app":       gw.Name,
					"component": "mcp-gateway",
				},
				"annotations": map[string]string{
					"nginx.ingress.kubernetes.io/proxy-read-timeout": "3600",
					"nginx.ingress.kubernetes.io/proxy-send-timeout": "3600",
				},
			},
			"spec": map[string]any{
				"rules": []any{
					map[string]any{
						"host": gw.IngressHost,
						"http": map[string]any{
							"paths": []any{
								map[string]any{
									"path":     "/",
									"pathType": "Prefix",
									"backend": map[string]any{
										"service": map[string]any{
											"name": gw.Name,
											"port": map[string]any{"number": gw.ServicePort},
										},
									},
								},
							},
						},
					},
				},
			},
		}
		if gw.IngressClassName != "" {
			ing["spec"].(map[string]any)["ingressClassName"] = gw.IngressClassName
		}
		if gw.TLSSecretName != "" {
			ing["spec"].(map[string]any)["tls"] = []any{
				map[string]any{
					"hosts":      []string{gw.IngressHost},
					"secretName": gw.TLSSecretName,
				},
			}
		}

		if err := writeYaml(filepath.Join(gwDir, "ingress.yaml"), ing); err != nil {
			return err
		}
		*resources = append(*resources, "gateway/ingress.yaml")
	}

	pdb := map[string]any{
		"apiVersion": "policy/v1",
		"kind":       "PodDisruptionBudget",
		"metadata": map[string]any{
			"name":      gw.Name,
			"namespace": namespace,
			"labels": map[string]string{
				"app":       gw.Name,
				"component": "mcp-gateway",
			},
		},
		"spec": map[string]any{
			"minAvailable": 1,
			"selector": map[string]any{
				"matchLabels": map[string]string{"app": gw.Name},
			},
		},
	}
	if err := writeYaml(filepath.Join(gwDir, "pdb.yaml"), pdb); err != nil {
		return err
	}
	*resources = append(*resources, "gateway/pdb.yaml")

	hpa := map[string]any{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
		"metadata": map[string]any{
			"name":      gw.Name,
			"namespace": namespace,
			"labels": map[string]string{
				"app":       gw.Name,
				"component": "mcp-gateway",
			},
		},
		"spec": map[string]any{
			"scaleTargetRef": map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"name":       gw.Name,
			},
			"minReplicas": gw.Replicas,
			"maxReplicas": 5,
			"metrics": []any{
				map[string]any{
					"type": "Resource",
					"resource": map[string]any{
						"name": "cpu",
						"target": map[string]any{
							"type":               "Utilization",
							"averageUtilization": 70,
						},
					},
				},
			},
		},
	}
	if err := writeYaml(filepath.Join(gwDir, "hpa.yaml"), hpa); err != nil {
		return err
	}
	*resources = append(*resources, "gateway/hpa.yaml")

	return nil
}
