package config

// Default configuration values
const (
	// DefaultDomain is the default domain for zdev projects
	// This is a wildcard domain that resolves to 127.0.0.1
	DefaultDomain = "0ploy.dev"

	// GlobalConfigFilename is the filename for the global config
	// Distinct from project config.yaml to avoid confusion
	GlobalConfigFilename = "global-config.yaml"

	// DNSResolverPort is the loopback host port the local DNS fallback
	// container publishes (container listens on 53). Deliberately NOT 5353
	// (macOS mDNSResponder/Bonjour holds that) and not privileged 53; the
	// host resolver config (/etc/resolver on macOS, systemd-resolved on
	// Linux) points at 127.0.0.1 on this port.
	DNSResolverPort = 5335
)

// Shared service image versions. Pin a full version, never latest or a
// floating tag: zdev pulls an image only when it is missing, so a floating
// tag stays at whatever it pointed to on a machine's first pull. Each pin
// carries a "renovate:" comment on the line above it, which Renovate's regex
// manager uses to propose updates (TestDefaultImagesPinned keeps the shape).
const (
	// RouterImage is the default Traefik router image
	// renovate: datasource=docker
	RouterImage = "traefik:v3.7.20"

	// MailImage is the default Mailpit image
	// renovate: datasource=docker
	MailImage = "axllent/mailpit:v1.34.0"

	// DBUIImage is the default Adminer image for database management
	// (the plain version tag is the standalone variant, like latest)
	// renovate: datasource=docker
	DBUIImage = "adminer:6.3.1"

	// RedisInsightsImage is the default Redis Insights image
	// renovate: datasource=docker
	RedisInsightsImage = "redis/redisinsight:3.10.2"

	// LogsImage is the default Dozzle log viewer image
	// renovate: datasource=docker
	LogsImage = "amir20/dozzle:v11.5.0"

	// DNSImage is the dnsmasq image used for the optional local DNS
	// fallback (see internal/resolver). It reads /etc/dnsmasq.conf, which
	// zdev generates and bind-mounts, and answers *.<domain> -> 127.0.0.1
	// authoritatively with no upstream. Only pulled when a user enables
	// the fallback for a rebinding-protecting router.
	// renovate: datasource=docker
	DNSImage = "dockurr/dnsmasq:2.93"

	// AlpineImage is the base of zdev's own helper images: the one-shot
	// containers that fill the agent volumes, and the locally built sandbox
	// bootstrap and fence (iptables) images. Those two are tagged by a hash
	// of their Dockerfile, so a bump here rebuilds them with current
	// packages.
	// renovate: datasource=docker
	AlpineImage = "alpine:3.24.4"

	// PiBuildImage builds pi for agent sandboxes (Node for its npm build, Bun
	// from the Bun pin below for the binaries).
	// renovate: datasource=docker
	PiBuildImage = "node:24.22.3-bookworm-slim"

	// TestImage is the lightweight image used for integration tests
	// Keep in sync with testdata/projects/*/config.yaml fixtures
	TestImage = "alpine:latest"
)

// Plugin versions
const (
	// StatiqPluginModule is the Go module path for the Statiq plugin
	StatiqPluginModule = "github.com/hhftechnology/statiq"

	// StatiqPluginVersion is the version of the Statiq plugin
	StatiqPluginVersion = "v1.0.1"
)

// Downloaded tools. Same rules as the images above: a full version, and the
// "renovate:" comment directly above `X = ToolRelease{Name: "...", Version:
// "...",` (one line), which Renovate's regex manager matches
// (TestToolsPinned keeps the shape). A version bump needs the new release's
// checksums for every platform listed.
var (
	// Mkcert creates the zdev CA and certificates; downloaded when not on PATH.
	// renovate: datasource=github-releases depName=FiloSottile/mkcert extractVersion=^v(?<version>.+)$
	Mkcert = ToolRelease{Name: "mkcert", Version: "1.4.4",
		URL: "https://github.com/FiloSottile/mkcert/releases/download/v{version}/mkcert-v{version}-{os}-{arch}",
		SHA256: map[string]string{
			"darwin/amd64": "a32dfab51f1845d51e810db8e47dcf0e6b51ae3422426514bf5a2b8302e97d4e",
			"darwin/arm64": "c8af0df44bce04359794dad8ea28d750437411d632748049d08644ffb66a60c6",
			"linux/amd64":  "6d31c65b03972c6dc4a14ab429f2928300518b26503f58723e532d1b0a3bbb52",
			"linux/arm64":  "b98f2cc69fd9147fe4d405d859c57504571adec0d3611c3eefd04107c7ac00d0",
		}}

	// Just runs deprecated .zdev/commands/*.just justfiles.
	// renovate: datasource=github-releases depName=casey/just extractVersion=^(?<version>.+)$
	Just = ToolRelease{Name: "just", Version: "1.59.0",
		URL:       "https://github.com/casey/just/releases/download/{version}/just-{version}-{arch}-{os}.tar.gz",
		OSNames:   map[string]string{"darwin": "apple-darwin", "linux": "unknown-linux-musl"},
		ArchNames: map[string]string{"amd64": "x86_64", "arm64": "aarch64"},
		SHA256: map[string]string{
			"darwin/amd64": "036ead9df9bc31dbae9934ae3b4236ed124d0bafcffd33439460ae084d5c9cb7",
			"darwin/arm64": "6574cea774ed4cae12e24b6aa11857de33e80dab4667cf628fd5840df96a2a9d",
			"linux/amd64":  "409044617b6f06517c1035e55ca38c8c856ff8b650e41bc2aa1ef82d5903a23e",
			"linux/arm64":  "23871b1702bfd550fb5f901778fd4290ecf2ee034a45a889fbe96d89b188086d",
		}}

	// Mutagen syncs project files on macOS.
	// renovate: datasource=github-releases depName=mutagen-io/mutagen extractVersion=^v(?<version>.+)$
	Mutagen = ToolRelease{Name: "mutagen", Version: "0.18.1",
		URL: "https://github.com/mutagen-io/mutagen/releases/download/v{version}/mutagen_{os}_{arch}_v{version}.tar.gz",
		SHA256: map[string]string{
			"darwin/amd64": "7d06f7d8fcfe90bc7e55cc834a2f2f20c2e0af9ea9bc35911fc4341ad56a9bbf",
			"darwin/arm64": "6f810416d9e5fc4fd5e18431146f8b3c5a2056ba5a24f76c1e66da86eb3257e2",
			"linux/amd64":  "7735286c778cc438418209f24d03a64f3a0151c8065ef0fe079cfaf093af6f8f",
			"linux/arm64":  "bcba735aebf8cbc11da9b3742118a665599ac697fa06bc5751cac8dcd540db8a",
		}}

	// Jq, Gh and Glab go into every agent sandbox (SandboxTools), Linux only.
	// renovate: datasource=github-releases depName=jqlang/jq extractVersion=^jq-(?<version>.+)$
	Jq = ToolRelease{Name: "jq", Version: "1.8.2",
		URL: "https://github.com/jqlang/jq/releases/download/jq-{version}/jq-linux-{arch}",
		SHA256: map[string]string{
			"linux/amd64": "b1c22172dd303f3be49e935aa56aa48a8b7a46e0bc838b4997d3bb451495870f",
			"linux/arm64": "8b85c817833814ddca00a144c33705546355afccf0cf39b188f3cdb48b852309",
		}}

	// renovate: datasource=github-releases depName=cli/cli extractVersion=^v(?<version>.+)$
	Gh = ToolRelease{Name: "gh", Version: "2.106.0",
		URL:         "https://github.com/cli/cli/releases/download/v{version}/gh_{version}_linux_{arch}.tar.gz",
		ArchivePath: "gh_{version}_linux_{arch}/bin/gh",
		SHA256: map[string]string{
			"linux/amd64": "a476449fa9625413c54a3734eded62e9e480e49f421ff08e7f3b3879c059a033",
			"linux/arm64": "5e8a0c51e1a8345f36bd0b8bb88604b6a5388df16f3ab3e3b9b6d794fa223a67",
		}}

	// renovate: datasource=gitlab-releases depName=gitlab-org/cli extractVersion=^v(?<version>.+)$
	Glab = ToolRelease{Name: "glab", Version: "1.133.0",
		URL:         "https://gitlab.com/gitlab-org/cli/-/releases/v{version}/downloads/glab_{version}_linux_{arch}.tar.gz",
		ArchivePath: "bin/glab",
		SHA256: map[string]string{
			"linux/amd64": "b17f858b8d700f1607f8ec02fe092a455da6a8dd18dac3eb6ccb09db1befd4bf",
			"linux/arm64": "a529fd254824b0f3f8d2350080c9b46633b67ff3e2b11455836d7a680c498b09",
		}}

	// Bun compiles pi for agent sandboxes (glibc and musl from one build).
	// renovate: datasource=github-releases depName=oven-sh/bun extractVersion=^bun-v(?<version>.+)$
	Bun = ToolRelease{Name: "bun", Version: "1.4.5",
		URL:       "https://github.com/oven-sh/bun/releases/download/bun-v{version}/bun-{os}-{arch}.zip",
		ArchNames: map[string]string{"amd64": "x64", "arm64": "aarch64"},
		SHA256: map[string]string{
			"linux/amd64": "94559b3b528995a49452f9f2404cec74f2efd7d06e0a000a8cd214b02c3666eb",
			"linux/arm64": "18656a3bf0ea5641f5796ae4213b75255579a70a0fded2f337933785568cdf84",
		}}

	// SandboxTools are added to every agent sandbox, in this order.
	SandboxTools = []ToolRelease{Jq, Gh, Glab}
)
