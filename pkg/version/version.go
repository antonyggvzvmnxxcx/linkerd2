package version

import (
	"errors"
	"fmt"
	"os"
)

// Version is updated automatically as part of the build process, and is the
// ground source of truth for the current process's build version.
//
// DO NOT EDIT
var Version = undefinedVersion

// ProxyInitVersion is the pinned version of the proxy-init, from
// https://github.com/linkerd/linkerd2-proxy-init This has to be kept in sync
// with the default version in the control plane's values.yaml.
var ProxyInitVersion = "v2.4.3"
var LinkerdCNIVersion = "v1.6.4"

const (
	// undefinedVersion should take the form `channel-version` to conform to
	// channelVersion functions.
	undefinedVersion = "dev-undefined"
)

func init() {
	// Use `$LINKERD_CONTAINER_VERSION_OVERRIDE` as the version only if the
	// version wasn't set at link time to minimize the chance of using it
	// unintentionally. This mechanism allows the version to be bound at
	// container build time instead of at executable link time to improve
	// incremental rebuild efficiency.
	if Version == undefinedVersion {
		override := os.Getenv("LINKERD_CONTAINER_VERSION_OVERRIDE")
		if override != "" {
			Version = override
		}
	}
}

// match compares two versions and returns success if they match, or an error
// with a contextual message if they do not.
func match(expectedVersion, actualVersion string) error {
	if expectedVersion == "" {
		return errors.New("expected version is empty")
	} else if actualVersion == "" {
		return errors.New("actual version is empty")
	}

	actual, err := parseChannelVersion(actualVersion)
	if err != nil {
		return fmt.Errorf("failed to parse actual version: %w", err)
	}
	expected, err := parseChannelVersion(expectedVersion)
	if err != nil {
		return fmt.Errorf("failed to parse expected version: %w", err)
	}

	if actual.channel != expected.channel {
		return fmt.Errorf("mismatched channels: running %s but retrieved %s",
			actual, expected)
	}

	if actual.version != expected.version || !actual.hotpatchEqual(expected) {
		return fmt.Errorf("is running version %s but the latest %s version is %s",
			actual.versionWithHotpatch(), actual.channel, expected.versionWithHotpatch())
	}

	return nil
}
