# Notices and third-party boundaries

littlep-deepseek is an unofficial community project, not affiliated with or endorsed by NetEase Youdao, DeepSeek or Rockchip. Their trademarks, services and software belong to their respective owners.

Apache-2.0 covers the project-authored Go bridge/supervisor, JS wrapper, Python/shell tools, tests, documentation and patch logic. It does not relicense any original or modified vendor ELF, firmware, bytecode, manifest, font, image, certificate or compiler. Users provide original files locally; generated derivative payloads and backups must not be added to the source repository.

## Dependency review

| Component | Usage | Distribution here / license |
|---|---|---|
| Go standard library / compiler | Bridge/supervisor build | No vendored Go source/toolchain; Go BSD license applies to the upstream toolchain/runtime, see https://go.dev/LICENSE |
| Python standard library | Patch/deploy/verify | Interpreter not distributed; PSF license applies upstream |
| Node.js built-in modules | Synthetic UI tests only | Runtime not distributed; upstream runtime licenses apply |
| QuickJS-compatible qjsc | Optional wrapper compilation | Not distributed; exact externally supplied tool required; vendor executable redistribution rights not established |
| Youdao original app / libraries | User-provided patch input | Not distributed or relicensed |
| DeepSeek API / native search protocol | HTTPS service | No SDK/model/harness code vendored; account/service terms apply |

No third-party Go modules, npm packages, C library sources or upstream Harness implementation are included. Protocol interoperability was implemented in project source. No third-party code is relabeled as Apache-2.0. Compiler rights and the user's local software modification/redistribution rights are separate from this source license.
