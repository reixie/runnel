.PHONY: setup build dev test vet verify
setup:
	./script/go.sh mod download
build:
	./script/build.sh
dev:
	./script/build_and_run.sh
test: build
	MYGO_GHOSTTY_VT="$(CURDIR)/dist/darwin-$(shell ./script/go.sh env GOARCH)/Runnel.app/Contents/Resources/libghostty-vt.dylib" ./script/go.sh test -count=1 -timeout=90s ./...
vet:
	./script/go.sh vet ./...
verify:
	./script/build_and_run.sh --verify
