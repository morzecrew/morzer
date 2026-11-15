package cli

import "github.com/morzecrew/morzer/internal/adapters/dockerdesktop"

// engineUser is who the engine reads the host's files as: on a Mac, the user
// who runs Docker Desktop, read at every call (RFC 0055 decision 19).
var engineUser = dockerdesktop.User
