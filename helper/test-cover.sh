set -x
go test -covermode count -coverprofile=coverage.txt || exit 1
curl -s https://codecov.io/bash | bash

# go tool cover -html=coverage.txt
