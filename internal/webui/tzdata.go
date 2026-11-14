//go:build webui

package webui

// The WebUI lets each account pick an IANA zone, so time.LoadLocation must work
// even where no zoneinfo database is installed: a scratch/minimal container
// image, a Windows dev box without %ZONEINFO%, or Alpine before apk installs
// tzdata. Embedding the database costs ~450 kB in the webui build only; the base
// build never links this package.
import _ "time/tzdata"
