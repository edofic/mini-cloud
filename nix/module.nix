{config, lib, pkgs, ...}: let
  inherit (lib) mkEnableOption mkIf mkOption types;
  cfg = config.services.mini-cloud;
  format = pkgs.formats.json {};
  configuration = format.generate "mini-cloud.json" (cfg.settings // {apps_dir = cfg.appsDir;});
in {
  options.services.mini-cloud = {
    enable = mkEnableOption "the mini-cloud application gateway";
    package = mkOption {type = types.package; description = "Gateway package.";};
    user = mkOption {type = types.str; default = "mini-cloud"; description = "Trusted deployment user running the gateway and all apps.";};
    group = mkOption {type = types.str; default = "mini-cloud"; description = "Gateway and apps group.";};
    appsDir = mkOption {type = types.str; default = "/var/lib/mini-cloud/apps"; description = "Authoritative, mutable application directory.";};
    home = mkOption {type = types.str; default = "/var/lib/mini-cloud"; description = "HOME and working directory for the service.";};
    runtimePackages = mkOption {
      type = types.listOf types.package;
      default = [pkgs.bash pkgs.bubblewrap pkgs.coreutils pkgs.python3];
      description = "Programs placed on PATH for applications and scheduled commands.";
    };
    environment = mkOption {type = types.attrsOf types.str; default = {}; description = "Additional environment variables shared by the gateway and apps.";};
    settings = mkOption {
      type = format.type;
      default = {};
      description = "Gateway JSON configuration. apps_dir is always taken from appsDir. This file is in the Nix store: do not put secrets here.";
    };
  };

  config = mkIf cfg.enable {
    services.mini-cloud.settings = {
      listen = lib.mkDefault "127.0.0.1:9080";
      base_domain = lib.mkDefault "apps.localhost";
      index_host = lib.mkDefault "apps.localhost";
      admin_host = lib.mkDefault "admin.apps.localhost";
    };
    users.users = mkIf (cfg.user == "mini-cloud") {
      mini-cloud = {isSystemUser = true; group = cfg.group; home = cfg.home;};
    };
    users.groups = mkIf (cfg.group == "mini-cloud") {mini-cloud = {};};
    systemd.tmpfiles.rules = [
      "d ${cfg.home} 0750 ${cfg.user} ${cfg.group} -"
      "d ${cfg.appsDir} 0750 ${cfg.user} ${cfg.group} -"
    ];
    systemd.services.mini-cloud = {
      description = "Mutable, on-demand application gateway";
      wantedBy = ["multi-user.target"];
      after = ["network.target"];
      path = cfg.runtimePackages;
      environment = {HOME = cfg.home;} // cfg.environment;
      serviceConfig = {
        ExecStart = "${lib.getExe cfg.package} -config ${configuration}";
        User = cfg.user;
        Group = cfg.group;
        WorkingDirectory = cfg.home;
        Restart = "always";
        RestartSec = "2s";
        KillMode = "control-group";
        TimeoutStopSec = "30s";
        UMask = "0077";
      };
    };
  };
}
