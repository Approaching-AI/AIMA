from pathlib import Path


def test_official_site_sync_script_uses_develop_and_atomic_latest() -> None:
    script = Path("scripts/sync-official-site-downloads.sh").read_text(encoding="utf-8")

    assert 'AIMA_SYNC_REF="${AIMA_SYNC_REF:-origin/develop}"' in script
    assert 'AIMA_DOWNLOAD_REMOTE_ROOT="${AIMA_DOWNLOAD_REMOTE_ROOT:-/root/aima-service/.data/aima-downloads}"' in script
    assert "--tags" not in script
    assert "--prune" not in script
    assert "+refs/heads/develop:refs/remotes/origin/develop" in script
    assert 'bash scripts/package-release.sh "$out_dir" >&2' in script
    assert "git worktree add --detach" in script
    assert "scripts/package-release.sh" in script
    assert "aima-linux-amd64" in script
    assert "aima-linux-arm64" in script
    assert "aima-darwin-arm64" in script
    assert "aima-windows-amd64.exe" in script
    assert "ln -sfn" in script
    assert "latest" in script


def test_official_site_sync_workflow_runs_on_develop_push() -> None:
    workflow = Path(".github/workflows/sync-official-site-downloads.yml").read_text(
        encoding="utf-8"
    )

    assert "branches:" in workflow
    assert "- develop" in workflow
    assert "make release-assets" in workflow
    assert "scripts/sync-official-site-downloads.sh" in workflow
    assert "AIMA_DOWNLOAD_SSH_KEY" in workflow
