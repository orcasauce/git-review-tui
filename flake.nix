{
  description = "A very basic flake";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs?ref=nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = {
    self,
    nixpkgs,
    flake-utils,
    ...
  }:
    flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs {inherit system;};
    in {
      apps.default = {
        type = "app";
        program = builtins.toString (
          pkgs.writeShellScript
          "${self.outputs.packages.${system}.default.name}-go_run"
          ''
            ${pkgs.go}/bin/go run main.go
          ''
        );
      };
      packages.default = pkgs.buildGoModule {
        pname = "git-review-tui";
        version = "1.6.5";
        src = ./.;
        vendorHash = "sha256-JKIFZxO3L0cDVbT8ZD/KtDzjBtTuBLkq/e3PSXZ0kaw=";
        buildInputs = [pkgs.git];
        nativeBuildInputs = [pkgs.git];

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
