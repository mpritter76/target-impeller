package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"github.com/target/impeller/constants"
	"github.com/target/impeller/types"
	"github.com/target/impeller/utils"
	"github.com/target/impeller/utils/commandbuilder"
)

const (
	kubectlBin = "kubectl"
)

var (
	kubeConfig = os.Getenv("HOME") + "/.kube/config"
)

type Plugin struct {
	ClusterConfig types.ClusterConfig
	ValueFiles    []string
	KubeConfig    string
	KubeContext   string
	Dryrun        bool
}

func (p *Plugin) Exec() error {
	// Init Kubernetes config
	if err := p.setupKubeconfig(); err != nil {
		return fmt.Errorf("Error initializing Kubernetes config: %v", err)
	}
	// Add configured repos
	for _, repo := range p.ClusterConfig.Helm.Repos {
		if err := p.addHelmRepo(repo); err != nil {
			return fmt.Errorf("Error adding Helm repo: %v", err)
		}
	}
	if err := p.updateHelmRepos(); err != nil {
		return fmt.Errorf("Error updating Helm repos: %v", err)
	}

	// Install addons
	for _, addon := range p.ClusterConfig.Releases {
		if err := p.installAddon(&addon); err != nil {
			return fmt.Errorf("Error installing addon \"%s\": %v", addon.Name, err)
		}
	}

	return nil
}

func (p *Plugin) addHelmRepo(repo types.HelmRepo) error {
	log.Println("Adding Helm repo:", repo.Name)
	cb := commandbuilder.CommandBuilder{Name: constants.HelmBin}
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: "repo"})
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: "add"})
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: repo.Name})
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: repo.URL})

	if repo.Username != nil {
		username, err := repo.Username.GetValue()
		if err != nil {
			return fmt.Errorf("Could not get username for repo: %v", err)
		}
		cb.Add(commandbuilder.Arg{
			Type:        commandbuilder.ArgTypeLongParam,
			Name:        "username",
			Value:       username,
			ValueSecret: true,
		})
	}

	if repo.Password != nil {
		password, err := repo.Password.GetValue()
		if err != nil {
			return fmt.Errorf("Could not get password for repo: %v", err)
		}
		cb.Add(commandbuilder.Arg{
			Type:        commandbuilder.ArgTypeLongParam,
			Name:        "password",
			Value:       password,
			ValueSecret: true,
		})
	}

	if err := cb.Run(); err != nil {
		return fmt.Errorf("Could not add repo \"%s\": %v", repo.Name, err)
	}
	return nil
}

func (p *Plugin) updateHelmRepos() error {
	log.Println("Updating Helm repos")
	cmd := exec.Command(constants.HelmBin, "repo", "update")
	if err := utils.Run(cmd, true); err != nil {
		return fmt.Errorf("Error updating helm repos: %v", err)
	}
	return nil
}

func (p *Plugin) installAddon(release *types.Release) error {
	log.Println("Installing addon:", release.Name, "@", release.Version)
	switch release.DeploymentMethod {
	case "kubectl":
		return p.installAddonViaKubectl(release)
	case "helm":
		fallthrough
	default:
		return p.installAddonViaHelm(release)
	}
}

// installAddonViaHelm installs addons via helm upgrade --install RELEASE CHART
func (p *Plugin) installAddonViaHelm(release *types.Release) error {
	cb := commandbuilder.CommandBuilder{Name: constants.HelmBin}
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: "upgrade"})
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: "--install"})
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: release.Name})
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: release.ChartPath})
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeLongParam, Name: "version", Value: release.Version})

	if p.ClusterConfig.Helm.Debug {
		cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: "--debug"})
	}

	// Add namespaces to command
	if release.Namespace != "" {
		cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeLongParam, Name: "namespace", Value: release.Namespace})
	}

	if p.ClusterConfig.Helm.LogLevel != 0 {
		cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeLongParam, Name: "v", Value: fmt.Sprint(p.ClusterConfig.Helm.LogLevel)})
	}

	// Add Overrides
	for _, override := range p.overrides(release) {
		cb.Add(override)
	}

	// Dry Run
	if p.Dryrun {
		log.Println("Running Dry run:", release.Name)
		cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: "--dry-run"})
	}

	// Execute helm upgrade
	if err := cb.Run(); err != nil {
		return fmt.Errorf("Error running helm: %v", err)
	}
	return nil
}

// installAddonViaKubectl installs addons via:
// helm fetch --version release.Version --untar release.ChartPath
// helm template $CHART | kubectl create -f -
func (p *Plugin) installAddonViaKubectl(release *types.Release) error {
	if err := p.fetchChart(release); err != nil {
		return fmt.Errorf("error fetching chart for kubectl deployment: %s", err)
	}

	renderedManifests, err := p.templateChart(release)
	if err != nil {
		return fmt.Errorf("error rendering chart for kubectl apply: %s", err)
	}

	// Dry Run
	if p.Dryrun {
		log.Println("Running Dry run:", release.Name)
		fmt.Printf("rendered chart output:\n%s", renderedManifests)
		return nil
	}

	cb := commandbuilder.CommandBuilder{Name: constants.KubectlBin}
	// kubectl apply -f -
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: "apply"})
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeLongParam, Name: "filename", Value: "-"})

	// Grab raw commandbuilder command so we can set stdin
	kubectlApplyCmd := cb.Command()
	kubectlApplyCmd.Stdin = strings.NewReader(renderedManifests)
	// We may need to run kubectl apply -f - twice if the helm chart
	// has dependant kubernetes resources. The first run will install
	// independent components and the second run will install the ones
	// that failed previously. If this command fails twice then the chart
	// is just broken
	if err := utils.Run(kubectlApplyCmd, false); err != nil {
		kubectlApplyCmd := cb.Command()
		kubectlApplyCmd.Stdin = strings.NewReader(renderedManifests)
		return utils.Run(kubectlApplyCmd, false)
	}
	return nil
}

func (p *Plugin) fetchChart(release *types.Release) error {
	cb := commandbuilder.CommandBuilder{Name: constants.HelmBin}
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: "fetch"})
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeLongParam, Name: "version", Value: release.Version})
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: "--untar"})
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: release.ChartPath})
	return cb.Run()
}

func (p *Plugin) templateChart(release *types.Release) (string, error) {
	chartDir := filepath.Base(release.ChartPath)
	cb := commandbuilder.CommandBuilder{Name: constants.HelmBin}
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: "template"})
	if release.Namespace != "" {
		cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeLongParam, Name: "namespace", Value: release.Namespace})
	}
	if release.Name != "" {
		cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeLongParam, Name: "name", Value: release.Name})
	}
	for _, override := range p.overrides(release) {
		cb.Add(override)
	}
	cb.Add(commandbuilder.Arg{Type: commandbuilder.ArgTypeRaw, Value: chartDir})
	cmd := cb.Command()
	templateBytes, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(templateBytes), nil
}

func (p *Plugin) setupKubeconfig() error {
	// Providing a Kubernetes config is mostly used for Drone support.
	// If not provided, the default `kubectl` search path is used.
	// WARNING: this may overwrite your config if it already exists.
	if p.KubeConfig != "" {
		log.Println("Creating Kubernetes config")
		if err := ioutil.WriteFile(kubeConfig, []byte(p.KubeConfig), 0644); err != nil {
			return fmt.Errorf("Error creating kube config file: %v", err)
		}
	}

	// Providing a Kubernetes config context is mostly used for Drone support.
	// If not provided, the current context from Kubernetes config is used.
	if p.KubeContext != "" {
		log.Println("Setting Kubernetes context")
		cmd := exec.Command(kubectlBin, "config", "use-context", p.KubeContext)
		if err := utils.Run(cmd, true); err != nil {
			return fmt.Errorf("Error setting Kubernetes context: %v", err)
		}
	}
	return nil
}

func (p *Plugin) helmInit(namespace string ) error {
	log.Printf("Initializing Helm..." )
	cmd := []string{"--debug"}

	if len(p.ClusterConfig.Helm.Overrides) > 0 {
		overrides := []string{}
		for overrideKey, overrideValue := range p.ClusterConfig.Helm.Overrides {
			overrides = append(overrides, fmt.Sprintf("'%v'='%v'", overrideKey, overrideValue))
		}
		cmd = append(cmd, "--override", strings.Join(overrides, ","))
	}


	// Initialization required for helm deployments
	if p.ClusterConfig.Helm.Upgrade {
		cmd = append(cmd, "--upgrade")
		cmd = append(cmd, "--force-upgrade")
	}
	if p.Dryrun {
		cmd = append(cmd, "--client-only")
	}

	if p.ClusterConfig.Helm.Debug {
		cmd = append(cmd, "--debug")
	}

	if p.ClusterConfig.Helm.ServiceAccount != "" {
		cmd = append(cmd, "--service-account", p.ClusterConfig.Helm.ServiceAccount)
	}

	if err := utils.Run(exec.Command(constants.HelmBin, cmd...), true); err != nil {
		return err
	}

	return nil
}

func (p *Plugin) overrides(release *types.Release) (args []commandbuilder.Arg) {
	// Add override files
	for _, fileName := range p.ValueFiles {
		log.Println("Adding override file:", fileName)
		args = append(args, commandbuilder.Arg{
			Type:  commandbuilder.ArgTypeShortParam,
			Name:  "f",
			Value: strings.TrimSpace(fileName)})
	}
	path := fmt.Sprintf("values/%s/default.yaml", release.Name)
	if _, err := os.Stat(path); err == nil {
		log.Println("Adding override file:", path)
		args = append(args, commandbuilder.Arg{
			Type:  commandbuilder.ArgTypeShortParam,
			Name:  "f",
			Value: path})
	}
	for _, path := range release.ValueFiles {
		if _, err := os.Stat(path); err != nil {
			log.Println("WARN: Value file does not exist:", path)
			continue
		}
		log.Println("Adding override file:", path)
		args = append(args, commandbuilder.Arg{
			Type:  commandbuilder.ArgTypeShortParam,
			Name:  "f",
			Value: path})
	}
	path = fmt.Sprintf("values/%s/%s.yaml", release.Name, p.ClusterConfig.Name)
	if _, err := os.Stat(path); p.ClusterConfig.Name != "" && err == nil {
		log.Println("Adding override file:", path)
		args = append(args, commandbuilder.Arg{
			Type:  commandbuilder.ArgTypeShortParam,
			Name:  "f",
			Value: path})
	}

	// Handle individual value overrides
	setValues := []string{}
	for _, override := range release.Overrides {
		log.Println("Overriding value for:", override.Target)
		overrideValue, err := override.GetValue()
		if err != nil {
			log.Println("WARNING: Could not get override value. Skipping override:", err)
			continue
		}
		if overrideValue == "" {
			log.Println("WARNING: Override value is blank.")
		}
		setValues = append(setValues, fmt.Sprintf("%s=%s", override.Target, overrideValue))
	}
	if len(setValues) > 0 {
		args = append(args, commandbuilder.Arg{
			Type:        commandbuilder.ArgTypeLongParam,
			Name:        "set",
			Value:       strings.Join(setValues, ","),
			ValueSecret: true})
	}
	return args
}
