{
  description = "Category Service - REST API for nested category hierarchies, packaged with Nix";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
   let
    version = "0.1.0";

    ldflags = [
      "-s"
      "-w"
      # The import path has to be the module path from go.mod, not the directory
      # name. A mismatch does not fail the build — the linker silently ignores an
      # -X that names no symbol — so the binary ships reporting "dev" forever and
      # nothing in the pipeline notices.
      "-X category-service/internal/version.Version=${version}"
    ];

    # Outputs that depend on the target system. Overlays are deliberately not
    # defined here: Nix validates `overlays` as a top-level attribute rather
    # than per system, so it is merged in at the bottom.
    perSystem = flake-utils.lib.eachDefaultSystem (system:
      let
        inherit (nixpkgs) lib;

        pkgs = import nixpkgs { inherit system; };

        # Only the files the build actually needs. Keeps the source filter and
        # the Docker build context small, and stops result symlinks or editor
        # droppings from leaking into a hash.
        src = lib.fileset.toSource {
          root = ./.;
          fileset = lib.fileset.unions [
            ./go.mod
            ./go.sum
            ./cmd
            ./internal
          ];
        };

        # go.mod pins go 1.27.1, but nixpkgs' default toolchain trails behind.
        # override injects the newer toolchain into buildGoModule, which pulls
        # it from its callPackage arguments rather than the attrs we pass.
        go = pkgs.go_1_27;

        category-server = pkgs.buildGoModule.override { inherit go; } {
          pname = "category-service";
          inherit version src;
          vendorHash = "sha256-BscqA2jheIqN2OwchEzG2gMzXOrYuDjFSxDKiau54s0=";
          subPackages = [ "cmd/server" ];
          inherit ldflags;

          # buildGoModule names the binary after its source directory, so rename
          # it to match pname and mainProgram.
          postInstall = ''
            if [ -x "$out/bin/server" ] && [ ! -e "$out/bin/category-service" ]; then
              mv "$out/bin/server" "$out/bin/category-service"
            fi
          '';

          env.CGO_ENABLED = "0";

          doCheck = true;

          meta = {
            description = "REST API service for managing nested category hierarchies";
            longDescription = ''
              A Go HTTP service that stores a category tree in PostgreSQL. It
              exposes a flat listing, a nested tree, ancestor paths and cascade
              deletes, and rejects updates that would create a cycle.
            '';
            license = lib.licenses.mit;
            mainProgram = "category-service";
            platforms = lib.platforms.unix;
          };
        };

        # Minimal runtime image: a static binary plus CA certificates for
        # outbound TLS. No shell, no package manager, no nix store.
        runtimeImage = pkgs.dockerTools.buildImage {
          name = "category-service";
          tag = "latest";

          copyToRoot = pkgs.buildEnv {
            name = "image-root";
            pathsToLink = [ "/bin" ];
            paths = [ pkgs.cacert category-server ];
          };

          config = {
            # A non-root user with no login shell, since nothing needs to exec in.
            User = "65532:65532";
            WorkingDir = "/app";
            ExposedPorts."8080/tcp" = { };
            Entrypoint = [ "${category-server}/bin/category-service" ];
            Env = [
              "APP_PORT=8080"
              "LOG_FORMAT=json"
              "LOG_LEVEL=info"
            ];
            Labels = {
              "org.opencontainers.image.title" = "category-service";
              "org.opencontainers.image.description" = category-server.meta.description;
              "org.opencontainers.image.version" = version;
              "org.opencontainers.image.licenses" = "MIT";
              "org.opencontainers.image.source" = "https://github.com/your-org/category-service";
            };
          };
        };

        # Streamed layers write straight to stdout, so `nix run` can pipe the
        # image into `docker load` without a multi-gigabyte tarball on disk.
        runtimeImageStream = pkgs.dockerTools.streamLayeredImage {
          name = "category-service";
          tag = version;

          contents = [ pkgs.cacert category-server ];

          config = {
            User = "65532:65532";
            WorkingDir = "/app";
            ExposedPorts."8080/tcp" = { };
            Entrypoint = [ "${category-server}/bin/category-service" ];
            Env = [
              "APP_PORT=8080"
              "LOG_FORMAT=json"
              "LOG_LEVEL=info"
            ];
          };
        };

        # A compose file that runs the Nix-built image instead of rebuilding it
        # with the Dockerfile, so there is a single build system. Compose only
        # orchestrates.
        composeFile = pkgs.writeText "category-service.compose.yml" ''
          # Load the image, then bring the stack up:
          #
          #   nix run .#load-image
          #   docker compose -f "$(nix eval --raw .#compose)" up -d
          services:
            app:
              image: category-service:${version}
              restart: unless-stopped
              ports:
                - "8080:8080"
              environment:
                DB_HOST: postgres
                DB_PORT: "5432"
                DB_USER: category_user
                DB_PASSWORD: category_pass
                DB_NAME: category_db
                DB_SSLMODE: disable
                APP_PORT: "8080"
                LOG_FORMAT: json
                LOG_LEVEL: info
              depends_on:
                postgres:
                  condition: service_healthy

          postgres:
            image: docker.io/library/postgres:17-alpine
            restart: unless-stopped
            environment:
              POSTGRES_USER: category_user
              POSTGRES_PASSWORD: category_pass
              POSTGRES_DB: category_db
            ports:
              - "5432:5432"
            volumes:
              - postgres_data:/var/lib/postgresql/data
            healthcheck:
              test: ["CMD-SHELL", "pg_isready -U category_user -d category_db"]
              interval: 5s
              timeout: 5s
              retries: 5

          volumes:
            postgres_data:
        '';
      in
      {
        packages = {
          default = category-server;
          inherit category-server;
          image = runtimeImage;
          image-stream = runtimeImageStream;
          compose = composeFile;
        };

        apps = {
          default = flake-utils.lib.mkApp {
            drv = category-server;
            name = "category-service";
          };

          # Streamed straight into the Docker daemon, so nothing is staged on disk.
          load-image = flake-utils.lib.mkApp {
            drv = pkgs.writeShellApplication {
              name = "load-image";
              runtimeInputs = [ pkgs.docker pkgs.skopeo ];
              text = ''
                echo "Streaming category-service:${version} into the Docker daemon..."
                ${runtimeImageStream} | docker load
                echo "Loaded category-service:${version}"
              '';
            };
            name = "load-image";
          };

          # Copies the image into a registry with skopeo, so no docker daemon is
          # involved. skopeo's docker-archive reader wants a real seekable file,
          # so the streamed image is unpacked to a temporary directory first.
          push-image = flake-utils.lib.mkApp {
            drv = pkgs.writeShellApplication {
              name = "push-image";
              runtimeInputs = [ pkgs.skopeo pkgs.gnupg ];

              text = ''
                workdir=$(mktemp -d)
                trap 'rm -rf "$workdir"' EXIT

                # Accept any signature type and identity: this checks that a
                # signature exists, not who produced it. The transport is local
                # and the content comes from the Nix store. skopeo has no flag
                # for this, so it goes in the config path it reads.
                export XDG_CONFIG_HOME="$workdir/config"
                mkdir -p "$XDG_CONFIG_HOME/containers"
                echo '{"default":[{"type":"insecureAcceptAnything"}]}' \
                  > "$XDG_CONFIG_HOME/containers/policy.json"

                # A scratch keyring, so the caller's own GPG keys are untouched.
                export GNUPGHOME="$workdir/gnupg"
                mkdir -p "$GNUPGHOME"
                chmod 700 "$GNUPGHOME"
                for key in ${pkgs.gnupg}/share/nix/builder-keys/*.key; do
                  gpg --batch --quiet --import "$key" 2>/dev/null || true
                done

                # streamLayeredImage already emits an uncompressed docker archive.
                archive="$workdir/image.tar"
                ${runtimeImageStream} > "$archive"

                # An argument without a transport prefix is treated as a registry
                # reference, so a bare host:port/repo works.
                target="''${1:-localhost:5000/category-service}:${version}"
                case "$target" in
                  *://*) ;;
                  *) target="docker://$target" ;;
                esac

                echo "Pushing $target..."
                skopeo copy --dest-tls-verify=false \
                  "docker-archive:$archive" "$target"
                echo "Pushed $target"
              '';
            };
            name = "push-image";
          };
        };

        devShells.default = pkgs.mkShell {
          name = "category-service-dev";

          packages = with pkgs; [
            go
            gopls
            gotools
            golangci-lint
            postgresql_17
            curl
            jq
            skopeo
            docker-client
          ];

          # Exported so `go run` and `go test` work without a wrapper script.
          DB_HOST = "localhost";
          DB_PORT = "5432";
          DB_USER = "category_user";
          DB_PASSWORD = "category_pass";
          DB_NAME = "category_db";
          DB_SSLMODE = "disable";
          APP_PORT = "8080";

          shellHook = ''
            cat <<'EOF'
            === Category Service dev shell ===

              go build ./...            build everything
              go test ./...             run tests
              go test -race ./...        run tests under the race detector
              go run ./cmd/server       start the API against localhost:5432

              nix build                  build the binary with Nix
              nix build .#image         build the container image
              nix run .#load-image      load the image into Docker
              nix run .#push-image      push the image to a registry

            PostgreSQL 17 is on PATH. Point DB_HOST at an instance to run the API.
            EOF
          '';
        };

        checks = {
          inherit category-server;

          image = runtimeImage;

          # Unit tests, run in the sandbox like any other build. subPackages is
          # empty so the test binary targets every package.
          unit-tests = pkgs.buildGoModule.override { inherit go; } {
            pname = "category-service-test";
            version = "0.0.0";
            inherit src;
            vendorHash = "sha256-BscqA2jheIqN2OwchEzG2gMzXOrYuDjFSxDKiau54s0=";
            doCheck = true;
            installPhase = "touch $out";
          };

          # Lint with the same configuration the devshell uses. Reusing
          # buildGoModule's fetcher phase means the module cache is populated
          # offline, which go/packages type checking needs.
          lint = pkgs.buildGoModule.override { inherit go; } {
            pname = "category-service-lint";
            version = "0.0.0";
            inherit src;
            vendorHash = "sha256-BscqA2jheIqN2OwchEzG2gMzXOrYuDjFSxDKiau54s0=";
            subPackages = [ "cmd/server" ];
            nativeBuildInputs = [ pkgs.golangci-lint ];
            env.CGO_ENABLED = "0";
            doCheck = false;
            installPhase = "touch $out";
            buildPhase = ''
              runHook preBuild
              golangci-lint run ./...
              runHook postBuild
            '';
          };

          # Formatting is enforced so the build fails on unformatted code.
          gofmt = pkgs.runCommand "check-gofmt"
            {
              nativeBuildInputs = [ go ];
            } ''
            cd ${src}
            unformatted=$(gofmt -l .)
            if [ -n "$unformatted" ]; then
              echo "these files are not gofmt-clean:" >&2
              echo "$unformatted" >&2
              echo "run: nix fmt or gofmt -w ." >&2
              exit 1
            fi
            touch $out
          '';

          # Vet needs the module cache, which only buildGoModule's fetcher
          # phase populates, so reuse it instead of a bare runCommand.
          govet = pkgs.buildGoModule.override { inherit go; } {
            pname = "category-service-vet";
            version = "0.0.0";
            inherit src;
            vendorHash = "sha256-BscqA2jheIqN2OwchEzG2gMzXOrYuDjFSxDKiau54s0=";
            subPackages = [ "cmd/server" ];
            # Vet compiles for the host, so the 1.27 toolchain is required
            # directly rather than through buildGoModule's default.
            nativeBuildInputs = [ go ];

            doCheck = false;
            installPhase = "touch $out";
            buildPhase = ''
              runHook preBuild
              go vet ./...
              runHook postBuild
            '';
          };
        };

        formatter = pkgs.nixpkgs-fmt;
      });

    # Lets another flake consume this package by adding it as an overlay:
    #   inputs.category-service.url = "github:your-org/category-service";
    #   nixpkgs.overlays = [ inputs.category-service.overlays.default ];
    overlay = final: _prev: {
      category-service = final.callPackage
        ({ lib, buildGoModule, go_1_27, ... }:
          buildGoModule.override { go = go_1_27; } {
            pname = "category-service";
            version = "${version}";
            src = self;
            vendorHash = "sha256-BscqA2jheIqN2OwchEzG2gMzXOrYuDjFSxDKiau54s0=";
            subPackages = [ "cmd/server" ];
            inherit ldflags;
            env.CGO_ENABLED = "0";
            postInstall = ''
              if [ -x "$out/bin/server" ] && [ ! -e "$out/bin/category-service" ]; then
                mv "$out/bin/server" "$out/bin/category-service"
              fi
            '';
            meta = {
              description = "REST API service for managing nested category hierarchies";
              license = lib.licenses.mit;
              mainProgram = "category-service";
            };
          })
        { };
    };
   in
    perSystem // { overlays.default = overlay; };
}
