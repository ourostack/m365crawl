package syncer

// A failed qualification remains unknown; it never invents an origin from the
// profile basename or the private copy's temporary directory.
func outlookSourceOrigin(path string) string {
	origin, err := peopleResolve(path)
	if err != nil {
		return ""
	}
	origin, err = peopleAbs(origin)
	if err != nil {
		return ""
	}
	return origin
}
