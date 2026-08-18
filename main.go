package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/michaelkosir/terraform-provider-utils/internal/provider"
)

var (
	// version is set by goreleaser at build time.
	version string = "dev"
)

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/michaelkosir/utils",
		Debug:   debug,
	}

	if err := providerserver.Serve(context.Background(), provider.New(version), opts); err != nil {
		log.Fatal(err.Error())
	}
}
