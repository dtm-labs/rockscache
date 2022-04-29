set -x
go test -covermode count -coverprofile=coverage.txt
curl -s https://codecov.io/bash | bash

# go tool cover -html=coverage.txt
