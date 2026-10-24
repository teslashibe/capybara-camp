package attachments

import "regexp"

var referenceID = regexp.MustCompile(`^a:[0-9]+(?:/[0-9]+){0,8}$`)
var hashID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (r Reference) Valid() bool {
	if len(r.ID) > 100 || !referenceID.MatchString(r.ID) || !hashID.MatchString(r.SourceDigest) || (r.Kind != "image" && r.Kind != "audio" && r.Kind != "document") {
		return false
	}
	if r.Source != "inline" && r.Source != "local" && r.Source != "remote" && r.Source != "provider" && r.Source != "unknown" {
		return false
	}
	if r.State == "embedded" {
		return r.Source == "inline" && allowedMediaType(r.MediaType, r.Kind) && r.Size > 0 && r.Size <= MaxBytes && hashID.MatchString(r.SHA256)
	}
	if r.SHA256 != "" || r.Size != 0 {
		return false
	}
	switch r.State {
	case "reference":
		return r.MediaType == "" && (r.Source == "local" || r.Source == "remote" || r.Source == "provider")
	case "unsupported":
		return r.MediaType == "" && (r.Source == "unknown" || r.Source == "inline")
	case "invalid", "too_large":
		if r.Source != "inline" {
			return false
		}
		if r.MediaType == "" {
			return r.State == "invalid"
		}
		return allowedMediaType(r.MediaType, r.Kind)
	}
	return false
}
