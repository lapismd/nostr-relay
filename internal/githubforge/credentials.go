// Package githubforge implements the github.com GitHub App adapter. Provider
// objects and credentials never escape this package.
package githubforge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var credentialName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var secretFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type CredentialStore interface {
	Load(string) ([]byte, error)
}

// RuntimeCredentials resolves named environment values or a single file below
// a fixed secret directory. It never accepts arbitrary paths.
type RuntimeCredentials struct {
	SecretDir string
	LookupEnv func(string) (string, bool)
}

func (store RuntimeCredentials) Load(reference string) ([]byte, error) {
	kind, name, found := strings.Cut(strings.TrimSpace(reference), ":")
	if !found || name == "" {
		return nil, fmt.Errorf("invalid credential reference")
	}
	switch kind {
	case "env":
		if !credentialName.MatchString(name) {
			return nil, fmt.Errorf("invalid environment credential reference")
		}
		lookup := store.LookupEnv
		if lookup == nil {
			lookup = os.LookupEnv
		}
		value, ok := lookup(name)
		if !ok || value == "" {
			return nil, fmt.Errorf("credential is unavailable")
		}
		return []byte(value), nil
	case "file":
		if !secretFileName.MatchString(name) {
			return nil, fmt.Errorf("invalid file credential reference")
		}
		directory := store.SecretDir
		if directory == "" {
			directory = "/run/secrets"
		}
		cleanDirectory := filepath.Clean(directory)
		path := filepath.Join(cleanDirectory, name)
		if filepath.Dir(path) != cleanDirectory {
			return nil, fmt.Errorf("credential file escapes secret directory")
		}
		value, err := os.ReadFile(path)
		if err != nil || len(value) == 0 {
			return nil, fmt.Errorf("credential is unavailable")
		}
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported credential reference")
	}
}

func ValidateCredentialReference(reference string) error {
	store := RuntimeCredentials{}
	kind, name, found := strings.Cut(strings.TrimSpace(reference), ":")
	if !found {
		return fmt.Errorf("invalid credential reference")
	}
	if kind == "env" && credentialName.MatchString(name) {
		return nil
	}
	if kind == "file" && secretFileName.MatchString(name) {
		return nil
	}
	_, err := store.Load(reference)
	return err
}
