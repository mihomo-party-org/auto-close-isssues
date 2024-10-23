## Auto Close Issue

### Usage

```yaml
name: Auto Close Issue
on:
  issues:
    types: [opened]

jobs:
    auto_close_issue:
        runs-on: ubuntu-latest
        steps:
        - name: Auto Close Issue
            uses: mihomo-party-org/auto-close-issues@main
            with:
                token: ${{ secrets.GITHUB_TOKEN }}
                url: 'https://api.openai.com'
                key: 'sk-xxxxxx'
                prompt: 'xxxxxx'
```
