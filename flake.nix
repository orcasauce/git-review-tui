{
  description = "A very basic flake";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs?ref=nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = {
    nixpkgs,
    flake-utils,
    ...
  }:
    flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs {inherit system;};
    in {
      packages.default = pkgs.buildGoModule {
        pname = "git-review-tui";
        version = "1.6.0";
        src = ./.; 
        vendorHash = "sha256-JkEFpZ3oUCV8ydg8f/yNWwkrDPIaoygPeSSvwx+0qTQ=";
        buildInputs = [ pkgs.git ];
        nativeBuildInputs = [ pkgs.git ];

        preCheck = ''
          export HOME=$(mktemp -d)
          git config --global user.name "nobody"
          git config --global user.email "nobody@example.com"
        '';
      };

      devShells.default = pkgs.mkShell {
        packages = [
          pkgs.go
        ];
      };
    });
}
