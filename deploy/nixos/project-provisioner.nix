{ config, lib, pkgs, ... }:
let
  cfg = config.services.workspaceMcpProjectProvisioner;
  reconciler = pkgs.writeShellApplication {
    name = "workspace-project-provisioner";
    runtimeInputs = with pkgs; [ bash kubectl kubernetes-helm curl jq coreutils ];
    text = ''
      export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
      export PWM_WORKER_IMAGE=${lib.escapeShellArg cfg.workerImage}
      export PWM_HELM_CHART=${lib.escapeShellArg (toString cfg.chart)}
      export PWM_HELM_BINARY=${lib.escapeShellArg "${pkgs.kubernetes-helm}/bin/helm"}
      export PWM_COMPUTE_NODES=${lib.escapeShellArg (lib.concatStringsSep " " cfg.computeNodes)}
      export PWM_EXCLUDE_PROJECTS=${lib.escapeShellArg (lib.concatStringsSep " " cfg.excludeProjects)}
      exec ${pkgs.bash}/bin/bash ${../../scripts/reconcile-kubernetes-projects.sh}
    '';
  };
in
{
  options.services.workspaceMcpProjectProvisioner = {
    enable = lib.mkEnableOption "isolated Workspace MCP Kubernetes workers for registered projects";
    workerImage = lib.mkOption {
      type = lib.types.str;
      example = "ghcr.io/example/workspace-worker@sha256:...";
      description = "A worker OCI image reachable or preloaded on every approved compute host.";
    };
    chart = lib.mkOption {
      type = lib.types.path;
      default = ../helm/project-workspace;
      description = "Workspace MCP Helm chart, with worker namespace isolation.";
    };
    computeNodes = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [];
      description = "Kubernetes hostnames approved for project worker placement.";
    };
    excludeProjects = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [];
      description = "Project IDs excluded from automatic compute provisioning.";
    };
    interval = lib.mkOption {
      type = lib.types.str;
      default = "2min";
      description = "Systemd interval for reconciling project workers.";
    };
  };
  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.workerImage != "" && cfg.computeNodes != [];
        message = "Workspace project provisioning needs a worker image and at least one compute node.";
      }
    ];
    systemd.services.workspace-project-provisioner = {
      description = "Reconcile isolated Workspace MCP project workers";
      requires = [ "k3s.service" ];
      after = [ "k3s.service" ];
      path = with pkgs; [ kubectl kubernetes-helm curl jq coreutils bash ];
      serviceConfig = {
        Type = "oneshot";
        User = "root";
        Group = "root";
        RuntimeDirectory = "workspace-project-provisioner";
        Environment = "HOME=/run/workspace-project-provisioner";
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        NoNewPrivileges = true;
        TimeoutStartSec = "30min";
      };
      script = "${reconciler}/bin/workspace-project-provisioner";
    };
    systemd.timers.workspace-project-provisioner = {
      description = "Reconcile Workspace MCP project workers periodically";
      wantedBy = [ "timers.target" ];
      timerConfig = {
        OnBootSec = "1min";
        OnUnitActiveSec = cfg.interval;
        Persistent = true;
      };
    };
  };
}
