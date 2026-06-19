package encoder

// registerDefaults populates reg with the curated encoder set. The
// per-family lists live in dedicated files (audio.go, software.go,
// vaapi.go, nvenc.go, qsv.go). This function is filled in incrementally
// as each family is ported to the Encoder interface.
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
	// nvencVideoEncoders, qsvVideoEncoders are added by their respective
	// files in later phases of the encoder-interface refactor.
}