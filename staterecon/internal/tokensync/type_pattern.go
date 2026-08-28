package tokensync

import "regexp"

var tokenTypePartPattern = regexp.MustCompile(`[a-z]+|[0-9]+`)
