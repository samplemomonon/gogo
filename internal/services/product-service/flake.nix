{
  description = "Product Service - Write side of the product domain";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils, ... }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;
        };

        go_1_27 = pkgs.go_1_27;

        # Get git commit and date for version stamping
        gitCommit = pkgs.runCommand "git-commit" {} ''
          git -C ${./.} rev-parse --short HEAD > $out || echo "unknown" > $out
        '';
        gitDate = pkgs.runCommand "git-date" {} ''
          git -C ${./.} log -1 --format=%cI > $out || echo "unknown" > $out
        '';

        version = "0.0.0";

        # Build the Go module using buildGoModule
        product-service = pkgs.buildGoModule rec {
          pname = "product-service";
          inherit version;

          src = pkgs.lib.cleanSourceWith {
            filter = path: type:
              !pkgs.lib.hasPrefix "/.git" path &&
              !pkgs.lib.hasPrefix "/.devenv" path &&
              !pkgs.lib.hasPrefix "/.direnv" path &&
              !pkgs.lib.hasPrefix "/result" path &&
              !pkgs.lib.hasPrefix "/bin" path;
            src = ./.;
          };

          vendorHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

          modRoot = "./";

          buildFlags = [ "-trimpath" ];

          ldflags = [
            "-s"
            "-w"
            "-X product-service/internal/version.Version=${version}"
            "-X product-service/internal/version.Commit=${builtins.readFile gitCommit}"
            "-X product-service/internal/version.Date=${builtins.readFile gitDate}"
          ];

          # Use go 1.27 explicitly
          go = go_1_27;

          # Native build dependencies
          nativeBuildInputs = [
            pkgs.git
            go_1_27
            pkgs.protobuf
          ];
        };

        # Docker image built from the Nix package
        dockerImage = pkgs.dockerTools.buildLayeredImage {
          name = "product-service";
          tag = product-service.version;
          contents = [ product-service pkgs.cacert pkgs.tzdata ];
          config = {
            Cmd = [ "/bin/product-service" ];
            WorkingDir = "/app";
            Env = [
              "APP_PORT=8082"
              "GRPC_PORT=9092"
              "LOG_FORMAT=json"
              "LOG_LEVEL=info"
            ];
            ExposedPorts = {
              "8082/tcp" = {};
              "9092/tcp" = {};
            };
            User = "65532:65532";
          };
        };
      in
      {
        packages = {
          inherit product-service;
          default = product-service;
        };
        dockerImage = dockerImage;
        devShells.default = pkgs.mkShell {
          buildInputs = [
            pkgs.git
            go_1_27
            pkgs.protobuf
            pkgs.postgresql
            pkgs.watchexec
          ];
          shellHook = ''
            export GREET="devenv"
            echo "Product Service development shell"
            echo "Run 'go run ./cmd/server' to start the service"
            echo "Run 'go test ./...' to run tests"
          '';
        };
      }
    );
}