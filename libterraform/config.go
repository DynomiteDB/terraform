// Copied from github.com/hashicorp/terraform/config.go
//
// Changes made:
// - Discover to discover PATH as well.

package libterraform

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl"
	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/terraform"
	"github.com/kardianos/osext"
)

// Config is the structure of the configuration for the Terraform CLI.
//
// This is not the configuration for Terraform itself. That is in the
// "config" package.
type Config struct {
	Providers    map[string]string
	Provisioners map[string]string

	DisableCheckpoint          bool `hcl:"disable_checkpoint"`
	DisableCheckpointSignature bool `hcl:"disable_checkpoint_signature"`
}

// BuiltinConfig is the built-in defaults for the configuration. These
// can be overridden by user configurations.
var BuiltinConfig Config

// ContextOpts are the global ContextOpts we use to initialize the CLI.
var ContextOpts terraform.ContextOpts

// LoadConfig loads the CLI configuration from ".terraformrc" files.
func LoadConfig(path string) (*Config, error) {
	// Read the HCL file and prepare for parsing
	d, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf(
			"Error reading %s: %s", path, err)
	}

	// Parse it
	obj, err := hcl.Parse(string(d))
	if err != nil {
		return nil, fmt.Errorf(
			"Error parsing %s: %s", path, err)
	}

	// Build up the result
	var result Config
	if err := hcl.DecodeObject(&result, obj); err != nil {
		return nil, err
	}

	return &result, nil
}

// Discover discovers plugins.
//
// This looks in the directory of the CWD, the executable and PATH, in that
// order for priority.
func (c *Config) Discover() error {
	// Look in the cwd.
	if err := c.discover("."); err != nil {
		return err
	}

	// Next, look in the same directory as the executable. Any conflicts
	// will overwrite those found in our current directory.
	exePath, err := osext.Executable()
	if err != nil {
		log.Printf("[ERR] Error loading exe directory: %s", err)
	} else {
		if err := c.discover(filepath.Dir(exePath)); err != nil {
			return err
		}
	}

	// Look in the PATH
	paths := os.Getenv("PATH")
	for _, p := range strings.Split(paths, ":") {
		if err := c.discover(p); err != nil {
			return err
		}
	}
	return nil
}

// Merge merges two configurations and returns a third entirely
// new configuration with the two merged.
func (c1 *Config) Merge(c2 *Config) *Config {
	var result Config
	result.Providers = make(map[string]string)
	result.Provisioners = make(map[string]string)
	for k, v := range c1.Providers {
		result.Providers[k] = v
	}
	for k, v := range c2.Providers {
		result.Providers[k] = v
	}
	for k, v := range c1.Provisioners {
		result.Provisioners[k] = v
	}
	for k, v := range c2.Provisioners {
		result.Provisioners[k] = v
	}

	return &result
}

func (c *Config) discover(path string) error {
	var err error

	if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			return err
		}
	}

	err = c.discoverSingle(
		filepath.Join(path, "terraform-provider-*"), &c.Providers)
	if err != nil {
		return err
	}

	err = c.discoverSingle(
		filepath.Join(path, "terraform-provisioner-*"), &c.Provisioners)
	if err != nil {
		return err
	}

	return nil
}

func (c *Config) discoverSingle(glob string, m *map[string]string) error {
	matches, err := filepath.Glob(glob)
	if err != nil {
		return err
	}

	if *m == nil {
		*m = make(map[string]string)
	}

	for _, match := range matches {
		file := filepath.Base(match)

		// If the filename has a ".", trim up to there
		if idx := strings.Index(file, "."); idx >= 0 {
			file = file[:idx]
		}

		// Look for foo-bar-baz. The plugin name is "baz"
		parts := strings.SplitN(file, "-", 3)
		if len(parts) != 3 {
			continue
		}

		log.Printf("[DEBUG] Discovered plugin: %s = %s", parts[2], match)
		(*m)[parts[2]] = match
	}

	return nil
}

// ProviderFactories returns the mapping of prefixes to
// ResourceProviderFactory that can be used to instantiate a
// binary-based plugin.
func (c *Config) ProviderFactories() map[string]terraform.ResourceProviderFactory {
	result := make(map[string]terraform.ResourceProviderFactory)
	fmt.Println("provider factory: ", c.Providers)
	for k, v := range c.Providers {
		fmt.Println("provider factory: ", k, v)
		result[k] = c.providerFactory(v)
	}

	return result
}

func (c *Config) providerFactory(path string) terraform.ResourceProviderFactory {
	// Build the plugin client configuration and init the plugin
	var config plugin.ClientConfig
	config.Cmd = pluginCmd(path)
	config.Managed = true
	client := plugin.NewClient(&config)

	return func() (terraform.ResourceProvider, error) {
		// Request the RPC client so we can get the provider
		// so we can build the actual RPC-implemented provider.
		rpcClient, err := client.Client()
		if err != nil {
			return nil, err
		}

		return rpcClient.ResourceProvider()
	}
}

// ProvisionerFactories returns the mapping of prefixes to
// ResourceProvisionerFactory that can be used to instantiate a
// binary-based plugin.
func (c *Config) ProvisionerFactories() map[string]terraform.ResourceProvisionerFactory {
	result := make(map[string]terraform.ResourceProvisionerFactory)
	for k, v := range c.Provisioners {
		result[k] = c.provisionerFactory(v)
	}

	return result
}

func (c *Config) provisionerFactory(path string) terraform.ResourceProvisionerFactory {
	// Build the plugin client configuration and init the plugin
	var config plugin.ClientConfig
	config.Cmd = pluginCmd(path)
	config.Managed = true
	client := plugin.NewClient(&config)

	return func() (terraform.ResourceProvisioner, error) {
		rpcClient, err := client.Client()
		if err != nil {
			return nil, err
		}

		return rpcClient.ResourceProvisioner()
	}
}

func pluginCmd(path string) *exec.Cmd {
	cmdPath := ""

	// If the path doesn't contain a separator, look in the same
	// directory as the Terraform executable first.
	if !strings.ContainsRune(path, os.PathSeparator) {
		exePath, err := osext.Executable()
		if err == nil {
			temp := filepath.Join(
				filepath.Dir(exePath),
				filepath.Base(path))

			if _, err := os.Stat(temp); err == nil {
				cmdPath = temp
			}
		}

		// If we still haven't found the executable, look for it
		// in the PATH.
		if v, err := exec.LookPath(path); err == nil {
			cmdPath = v
		}
	}

	// If we still don't have a path, then just set it to the original
	// given path.
	if cmdPath == "" {
		cmdPath = path
	}

	// Build the command to execute the plugin
	return exec.Command(cmdPath)
}
