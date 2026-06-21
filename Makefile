PLUGIN_NAME := cpa-516-blocker
DIST_DIR := dist

.PHONY: build test clean

build:
	mkdir -p $(DIST_DIR)
	go build -buildmode=c-shared -o $(DIST_DIR)/$(PLUGIN_NAME).so .

test:
	go test ./...

clean:
	rm -rf $(DIST_DIR)
