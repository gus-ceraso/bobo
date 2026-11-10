module ceraso.dev/bobo

go 1.27.1

require (
	github.com/creack/pty v1.1.24
	github.com/disintegration/imaging v1.6.2
	github.com/hinshun/vt10x v0.0.0-20220301184237-5011da428d02
	github.com/reeflective/readline v1.3.0
	go.yaml.in/yaml/v3 v3.0.4
	golang.org/x/image v0.47.0
	golang.org/x/sys v0.49.0
)

require github.com/rivo/uniseg v0.4.7 // indirect

replace github.com/reeflective/readline => github.com/gus-ceraso/readline v1.3.1-0.20261110003744-601a08de05ec
