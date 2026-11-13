{
  description = "Go ERIS library";
  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    pre-commit-hooks = {
      url = "github:cachix/pre-commit-hooks.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };
  outputs = {
    self,
    nixpkgs,
    flake-utils,
    pre-commit-hooks,
    ...
  }:
    flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs {
        inherit system;
      };
      goToolchain = pkgs.go_1_27.overrideAttrs (_: {
        version = "1.27.4";
        src = pkgs.fetchurl {
          url = "https://go.dev/dl/go1.27.4.src.tar.gz";
          hash = "sha256-c8+4VxrsDAEXdofbhxY9Rqog6XeWcncokaoXZU2aYJk=";
        };
      });

      # mdformat 0.7.22 works with markdown-it-py 4, but its package metadata
      # still has an upper bound of version 3.
      markdownPython = pkgs.python3.override {
        packageOverrides = _: previous: {
          mdformat = previous.mdformat.overridePythonAttrs (_: {
            pythonRelaxDeps = [ "markdown-it-py" ];
          });
        };
      };
      markdownFormatter = markdownPython.withPackages (
        pythonPackages: with pythonPackages; [
          mdformat
          mdformat-gfm
          mdformat-gfm-alerts
        ]
      );

      checks = {
        pre-commit-check = pre-commit-hooks.lib.${system}.run {
          src = ./.;
          hooks = {
            # gotest.enable = true;
            commitizen.enable = true;
            typos.enable = true;
            mdformat = {
              enable = true;
              package = markdownFormatter;
              entry = "${markdownFormatter}/bin/mdformat --wrap 80";
            };
            typos-commit = {
              enable = true;
              description = "Find typos in commit message";
              entry = let script = pkgs.writeShellScript "typos-commit" ''
                typos "$1"
              ''; in builtins.toString script;
              stages = [ "commit-msg" ];
            };
            govet.enable = true;
            gofmt.enable = true;
            golangci-lint.enable = true;
            gotidy = {
              enable = true;
              description = "Makes sure go.mod matches the source code";
              entry = let script = pkgs.writeShellScript "gotidyhook" ''
                go mod tidy -v
              ''; in builtins.toString script;
              stages = [ "pre-commit" ];
            };
            govulncheck = {
              enable = true;
              description = "Run govulncheck";
              pass_filenames = false;
              entry = let script = pkgs.writeShellScript "govulncheckhook" ''
                go run ./internal/vulncheck
              ''; in builtins.toString script;
              stages = [ "pre-commit" ];
            };
          };
        };
      };
    in {
      devShells.default = pkgs.mkShell {
        inherit (checks.pre-commit-check) shellHook;
        buildInputs = with pkgs; [
          goToolchain
          golangci-lint
          gopls
          govulncheck

          coverage-reporter

          typos
          commitizen
          just
          markdownFormatter
        ];
      };
    });
}
