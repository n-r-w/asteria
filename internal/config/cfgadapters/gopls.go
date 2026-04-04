package cfgadapters

// GoplsConfig holds gopls-specific settings loaded from environment variables.
type GoplsConfig struct {
	BuildFlags []string
	Env        map[string]string
}

type envConfigGopls struct {
	GoplsBuildFlags []string `env:"ASTERIAMCP_GOPLS_BUILD_FLAGS" envSeparator:";"`
	GoplsEnv        []string `env:"ASTERIAMCP_GOPLS_ENV" envSeparator:";"`
}

// build converts gopls-specific environment variables into runtime configuration.
func (e envConfigGopls) build() (GoplsConfig, error) {
	buildFlags := trimNonEmptyEntries(e.GoplsBuildFlags)
	envEntries := trimNonEmptyEntries(e.GoplsEnv)
	env, err := parseKeyValueEntries(envEntries, "ASTERIAMCP_GOPLS_ENV")
	if err != nil {
		return GoplsConfig{}, err
	}

	return GoplsConfig{
		BuildFlags: buildFlags,
		Env:        env,
	}, nil
}
