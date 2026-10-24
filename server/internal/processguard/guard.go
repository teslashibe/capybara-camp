// Package processguard confines cloud provider processes and their descendants.
// The parent worker remains outside the domain so it can manage its private journal.
package processguard

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

var ErrUnavailable = errors.New("The cloud runtime cannot enforce provider process isolation")
var ErrPolicy = errors.New("Invalid provider process isolation policy")

type Policy struct {
	Home           string   `json:"home"`
	Workspace      string   `json:"workspace"`
	WriteWorkspace bool     `json:"write_workspace"`
	ReadFiles      []string `json:"read_files,omitempty"`
}

func (p Policy) Validate() error {
	for _, value := range []string{p.Home, p.Workspace} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" {
			return ErrPolicy
		}
		info, err := os.Lstat(value)
		if err != nil || !info.IsDir() {
			return ErrPolicy
		}
	}
	if len(p.ReadFiles) > 8 {
		return ErrPolicy
	}
	for _, file := range p.ReadFiles {
		if !filepath.IsAbs(file) || filepath.Clean(file) != file {
			return ErrPolicy
		}
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() {
			return ErrPolicy
		}
	}
	return nil
}

// Wrap uses the trusted runner binary, not a program located in the checkout.
// Policy data contains paths and access modes only, never tokens or prompts.
func Wrap(executable string, args []string, policy Policy) (string, []string, error) {
	if !filepath.IsAbs(executable) || len(args) == 0 {
		return "", nil, ErrPolicy
	}
	if err := policy.Validate(); err != nil {
		return "", nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return "", nil, ErrUnavailable
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return "", nil, ErrPolicy
	}
	wrapped := []string{"process-guard", base64.RawURLEncoding.EncodeToString(raw), executable}
	wrapped = append(wrapped, args[1:]...)
	return self, wrapped, nil
}
func Run(args []string) error {
	if len(args) < 2 || len(args[0]) > 8192 || !filepath.IsAbs(args[1]) {
		return ErrPolicy
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(args[0])
	if err != nil {
		return ErrPolicy
	}
	var p Policy
	if json.Unmarshal(raw, &p) != nil || p.Validate() != nil {
		return ErrPolicy
	}
	return restrictAndExec(p, args[1:])
}
