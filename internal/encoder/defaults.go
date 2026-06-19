package encoder

// registerDefaults populates reg with the curated encoder set. The
// per-family lists live in dedicated files (audio.go, software.go,
// vaapi.go, nvenc.go, qsv.go). The order here only matters as a
// fallback for equal-priority slots; the registry sorts by priority
// descending then name ascending.
func registerDefaults(reg *Registry) {
	for _, e := range audioEncoders() {
		reg.Add(e)
	}
	for _, e := range softwareVideoEncoders() {
		reg.Add(e)
	}
	for _, e := range vaapiVideoEncoders() {
		reg.Add(e)
	}
	for _, e := range nvencVideoEncoders() {
		reg.Add(e)
	}
	for _, e := range qsvVideoEncoders() {
		reg.Add(e)
	}
}
