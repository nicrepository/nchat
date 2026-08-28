package buildinfo

var Version = "0.0.0"
var Commit = "dev"

type Info struct {
	Version string
	Commit  string
}

func Current() Info {
	return Info{
		Version: defaultString(Version, "0.0.0"),
		Commit:  defaultString(Commit, "dev"),
	}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
