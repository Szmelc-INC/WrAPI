{
  description = "WrAPI — AI chat wrapper API (Google AI Mode, Meta AI on WhatsApp Web) with an OpenAI-compatible localhost server";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        wrapi = pkgs.buildGoModule {
          pname = "wrapi";
          version = "0.4.0";
          src = self;
          # Dependencies are vendored in-repo (go mod vendor), so no
          # separate vendor hash is needed.
          vendorHash = null;
          subPackages = [ "cmd/wrapi" ];
          CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" ];
          postInstall = ''
            install -Dm644 -t $out/share/wrapi/drivers ${self}/drivers/*.json
            install -Dm644 ${self}/LICENSE $out/share/licenses/wrapi/LICENSE
            substitute ${./packaging/wrapi.service} $out/lib/systemd/user/wrapi.service \
              --replace /usr/bin/wrapi $out/bin/wrapi \
              --replace /usr/share/wrapi/drivers $out/share/wrapi/drivers
          '';
          # One smoke check that the binary runs.
          doCheck = false;
          meta = with pkgs.lib; {
            description = "AI chat wrapper API with an OpenAI-compatible localhost server (Google AI Mode, Meta AI via WhatsApp Web)";
            homepage = "https://github.com/Szmelc-INC/WrAPI";
            license = licenses.mit;
            mainProgram = "wrapi";
            platforms = platforms.linux;
          };
        };
      in {
        packages.default = wrapi;
        packages.wrapi = wrapi;

        apps.default = {
          type = "app";
          program = "${wrapi}/bin/wrapi";
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [ go gopls ];
        };
      });
}
