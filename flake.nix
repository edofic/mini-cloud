{
  description = "A mutable, on-demand application gateway for one Unix machine";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = {self, nixpkgs}: let
    systems = ["x86_64-linux" "aarch64-linux" "aarch64-darwin"];
    forAllSystems = nixpkgs.lib.genAttrs systems;
  in {
    packages = forAllSystems (system: let
      pkgs = nixpkgs.legacyPackages.${system};
    in {
      default = self.packages.${system}.mini-cloud;
      mini-cloud = pkgs.buildGo127Module {
        pname = "mini-cloud";
        version = "0.1.0";
        src = nixpkgs.lib.cleanSource ./.;
        vendorHash = null;
        env.CGO_ENABLED = 0;
        buildFlags = ["-buildvcs=false"];
        meta = {
          description = "Mutable, on-demand application gateway";
          homepage = "https://github.com/edofic/mini-cloud";
          license = nixpkgs.lib.licenses.asl20;
          platforms = systems;
          mainProgram = "mini-cloud";
        };
      };
    });

    devShells = forAllSystems (system: let
      pkgs = nixpkgs.legacyPackages.${system};
    in {
      default = pkgs.mkShell {
        packages = [pkgs.go_1_27 pkgs.golangci-lint pkgs.python3 pkgs.curl pkgs.bash pkgs.stdenv.cc]
          ++ pkgs.lib.optionals pkgs.stdenv.isLinux [pkgs.bubblewrap];
      };
    });

    nixosModules.default = {pkgs, lib, ...}: {
      imports = [./nix/module.nix];
      services.mini-cloud.package = lib.mkDefault self.packages.${pkgs.stdenv.hostPlatform.system}.default;
    };
    nixosModules.mini-cloud = self.nixosModules.default;
  };
}
