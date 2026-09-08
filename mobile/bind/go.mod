module github.com/PLASMA-FR/relay/mobile/bind

go 1.26.0

require github.com/PLASMA-FR/relay v0.1.0

require (
	golang.org/x/mobile v0.0.0-20260821190718-4776eadac327 // indirect
	golang.org/x/mod v0.39.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
)

replace github.com/PLASMA-FR/relay => ../..

tool (
	golang.org/x/mobile/cmd/gobind
	golang.org/x/mobile/cmd/gomobile
)
