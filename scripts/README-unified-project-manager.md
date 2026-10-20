# Unified GitHub Project Manager

<!-- file: scripts/README-unified-project-manager.md -->
<!-- version: 1.0.1 -->
<!-- guid: 5c6d7e8f-9a0b-1234-5678-9abcdef01234 -->
<!-- last-edited: 2026-10-20 -->

A comprehensive Python script that manages GitHub Projects across all
repositories in the organization. This script consolidates and replaces all
individual project creation scripts.

## 🎯 Overview

The Unified GitHub Project Manager provides:

- **Single Source of Truth**: All project definitions in one place
- **Multi-Repository Support**: Projects can span multiple repositories
- **Idempotent Operations**: Safe to run multiple times
- **Issue Assignment**: Automatic assignment of existing issues
- **Workflow Setup**: Built-in GitHub automation configuration
- **Dry-Run Mode**: Preview changes before executing

## 📋 Managed Projects

### Cross-Repository Projects

- **gcommon Refactor**: Migration to gcommon modules (gcommon, subtitle-manager)
- **Security & Logging**: Security enhancements (gcommon, subtitle-manager,
  ghcommon)

### Repository-Specific Projects

- **Subtitle Manager Development**: Core media processing features
- **Metadata Editor**: Metadata editing interface
- **Whisper Container Integration**: ASR service integration
- **ghcommon Cleanup**: Infrastructure cleanup tasks
- **ghcommon Core Improvements**: Workflow enhancements
- **ghcommon Testing & Quality**: Testing and validation
- **gCommon Development**: Go libraries and protobuf
- **Codex CLI Cleanup**: AI tooling cleanup
- **Codex CLI Core Improvements**: Enhanced automation
- **Codex CLI Testing & Quality**: AI workflow validation

## 🚀 Usage

### Basic Commands

```bash
# List all projects and their status
python3 scripts/unified_github_project_manager.py --list-projects

# Preview what would be created/updated
python3 scripts/unified_github_project_manager.py --dry-run

# Run full project setup
python3 scripts/unified_github_project_manager.py

# Verbose output for debugging
python3 scripts/unified_github_project_manager.py --verbose

# Setup workflows only (for existing projects)
python3 scripts/unified_github_project_manager.py --setup-workflows
```

### Advanced Usage

```bash
# Force update existing projects
python3 scripts/unified_github_project_manager.py --force

# Dry-run with verbose output
python3 scripts/unified_github_project_manager.py --dry-run --verbose
```

## 🔧 Features

### Idempotent Operations

- Detects existing projects and skips creation
- Safe to run multiple times
- No duplicate projects or links

### Multi-Repository Linking

- Projects can span multiple repositories
- Automatic repository linking
- Cross-project issue management

### Issue Assignment

- Predefined issue mappings
- Automatic assignment to appropriate projects
- Repository-aware issue URLs

### Workflow Automation

- Built-in workflow configuration
- Auto-close on completion
- Pull request merge automation
- Label-based auto-add rules

## 📝 Auto-Add Workflow Configuration

The script provides label-based auto-add configurations for GitHub Actions:

```yaml
# Example auto-add workflow mapping
gcommon Refactor: gcommon, protobuf, refactor, migration
Security & Logging: security, logging, audit, compliance
Metadata Editor: metadata, editor, ui, search
```

Use these configurations in your `.github/workflows/add-to-project.yml` files.

## 🔍 Project Structure

Each project is defined with:

```python
{
    "description": "Project description",
    "repositories": ["repo1", "repo2"],
    "labels": ["label1", "label2"],
    "auto_close_enabled": True,
    "pr_merge_enabled": True,
}
```

## 📊 Issue Mappings

Specific issues are automatically assigned:

```python
{
    "gcommon Refactor": [
        "subtitle-manager:1255",  # gcommon integration
        "subtitle-manager:891",  # protobuf migration
    ],
    "Metadata Editor": [
        "subtitle-manager:1135",  # metadata interface
        "subtitle-manager:1330",  # search improvements
    ],
    # ... more mappings
}
```

## 🛠️ Prerequisites

1. **Python 3.7+**
2. **GitHub CLI** installed and authenticated
3. **Required Permissions**:
   - `repo` scope for repository access
   - `project` scope for project management
   - `read:project` scope for project reading

### Setup Authentication

```bash
# Install GitHub CLI
brew install gh  # macOS
# or apt install gh  # Linux

# Authenticate with required scopes
gh auth login
gh auth refresh -s project,read:project
```

## 🔄 Migration from Old Scripts

This script replaces the following repository-specific scripts:

- `subtitle-manager/scripts/create-github-projects.sh`
- `gcommon/scripts/setup-github-projects.sh`
- `ghcommon/scripts/create-projects.sh`
- `codex-cli/scripts/create-github-projects.sh`
- `subtitle-manager/scripts/manage-project-structure.sh`

### Migration Steps

1. **Run the unified script** from `ghcommon`:

   ```bash
   cd ghcommon
   python3 scripts/unified_github_project_manager.py --dry-run
   python3 scripts/unified_github_project_manager.py
   ```

2. **Remove old scripts** from other repositories:

   ```bash
   # In each repository
   rm scripts/create-github-projects.sh
   rm scripts/setup-github-projects.sh
   rm scripts/manage-project-structure.sh
   ```

3. **Update documentation** to reference the unified script

## 🐛 Troubleshooting

### Common Issues

**Authentication Error**:

```bash
gh auth refresh -s project,read:project
```

**Project Not Found**:

- Check project exists with `--list-projects`
- Verify organization/user ownership

**Permission Denied**:

- Ensure GitHub CLI has project scopes
- Check repository access permissions

### Debug Mode

Run with `--verbose` for detailed logging:

```bash
python3 scripts/unified_github_project_manager.py --verbose --dry-run
```

### Log Files

Check `unified_project_manager.log` for detailed execution logs.

## 🎯 Best Practices

1. **Always test with `--dry-run` first**
2. **Use `--verbose` for troubleshooting**
3. **Check `--list-projects` before major changes**
4. **Run from the `ghcommon` repository**
5. **Ensure authentication is current**

## 🔮 Future Enhancements

- [ ] GraphQL API integration for workflow automation
- [ ] Custom field management
- [ ] Automated issue labeling
- [ ] Project template support
- [ ] Bulk issue operations
- [ ] Project analytics and reporting

## 📚 Related Documentation

- [GitHub Projects Documentation](https://docs.github.com/en/issues/planning-and-tracking-with-projects)
- [GitHub CLI Project Commands](https://cli.github.com/manual/gh_project)
- [GitHub GraphQL API](https://docs.github.com/en/graphql)

## 🤝 Contributing

When adding new projects or repositories:

1. Update `_get_project_definitions()` method
2. Add issue mappings in `_get_issue_mappings()` if needed
3. Test with `--dry-run` before committing
4. Update this documentation

## 📄 License

MIT License - See LICENSE file for details.
