package deployment

import (
	"os"
	"strings"
)

const KindEnv = "MULTICA_DEPLOYMENT_KIND"

type Kind string

const (
	KindUnknown  Kind = ""
	KindDev      Kind = "dev"
	KindCloud    Kind = "cloud"
	KindSelfHost Kind = "self_host"
)

func KindFromEnv() Kind {
	return NormalizeKind(os.Getenv(KindEnv))
}

func IsSelfHostFromEnv() bool {
	return KindFromEnv() == KindSelfHost
}

func NormalizeKind(raw string) Kind {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "self_host", "self-host", "selfhost":
		return KindSelfHost
	case "cloud", "hosted", "managed":
		return KindCloud
	case "dev", "development", "local", "test":
		return KindDev
	default:
		return KindUnknown
	}
}
