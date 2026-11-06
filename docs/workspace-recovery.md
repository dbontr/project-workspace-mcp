# Workspace checkpoint recovery

Use `workspace_checkpoint_stop` and wait for `stopped`. Keep its snapshot ID.
Create a new ready workspace in the same project, then call `workspace_restore`
with the new workspace ID and original snapshot ID. Verify the restored file
checksums before allowing work to resume.

Kubernetes workspace Pods run without root privileges. The worker strips the
archive's leading `./` component so GNU tar never alters PVC mount-root
ownership, permissions, or timestamps. Retain the snapshot and the original
volume until recovery is verified; prune only with explicit authorization.
