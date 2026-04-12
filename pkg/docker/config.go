package docker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/types"
	"github.com/docker/docker/pkg/homedir"
)

const (
	dockerFileFolder               = ".docker"
	AzureContainerRegistryUsername = "00000000-0000-0000-0000-000000000000"
)

func GetAuthConfig(host string) (*Credentials, error) {
	dockerConfig, err := loadDockerConfig()
	if err != nil {
		return nil, err
	}

	if host == "registry-1.docker.io" {
		host = "https://index.docker.io/v1/"
	}
	ac, err := dockerConfig.GetAuthConfig(host)
	if err != nil {
		return nil, fmt.Errorf("get auth config for host %s: %w", host, err)
	}

	return prepareCredentials(host, ac), nil
}

func prepareCredentials(host string, authConfig types.AuthConfig) *Credentials {
	if authConfig.Password == "" && authConfig.IdentityToken != "" {
		authConfig.Password = authConfig.IdentityToken
	}

	if authConfig.Username == "" && isAzureContainerRegistry(authConfig.ServerAddress) {
		authConfig.Username = AzureContainerRegistryUsername
	}

	return &Credentials{
		ServerURL: host,
		Username:  authConfig.Username,
		Secret:    authConfig.Password,
	}
}

func loadDockerConfig() (*configfile.ConfigFile, error) {
	configDir := os.Getenv("DOCKER_CONFIG")
	if configDir == "" {
		configDir = filepath.Join(homedir.Get(), dockerFileFolder)
	}

	return config.Load(configDir)
}

func isAzureContainerRegistry(serverAddress string) bool {
	return strings.HasSuffix(serverAddress, "azurecr.io")
}
