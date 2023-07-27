BINARY=stream-downloader
OS_ARCHS=linux/amd64 windows/amd64 darwin/amd64 darwin/arm64

.PHONY: all $(OS_ARCHS)
all: $(OS_ARCHS)

$(OS_ARCHS):
	env GOOS=$(word 1,$(subst /, ,$@)) GOARCH=$(word 2,$(subst /, ,$@)) go build -o $(BINARY)-$(word 1,$(subst /, ,$@))-$(word 2,$(subst /, ,$@))