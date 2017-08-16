bootstrap:
	glide install
	glide update --strip-vendor

build:
	cd cmd/ns-initializer && go build

test:
	go test -cover $(shell go list ./... | grep -v /vendor/)

clean:
	rm -f cmd/ns-initializer/ns-initializer
