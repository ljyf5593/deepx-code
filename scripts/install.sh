#!/usr/bin/env bash
# deepx one-click installer
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/itmisx/deepx-code/main/scripts/install.sh | bash
#   bash scripts/install.sh [--version vX.Y.Z] [--prefix ~/.local] [--from-source]
#
# 默认:从 GitHub Releases 拉取最新预编译二进制到 ~/.local/bin/deepx,并把 ~/.local/bin
# 加入 shell PATH。
#
# --from-source 走 go build(需本机 Go 1.25+),适合开发者。

set -euo pipefail

# ---------------------------------------------------------------------------
# 配置:仓库 + 下载来源。
#   SOURCE=github(默认)/ gitee —— 国内用户用 gitee 加速:
#     curl -fsSL <install.sh> | SOURCE=gitee bash
#   OWNER/REPO 可用 DEEPX_OWNER/DEEPX_REPO 覆盖(两边仓库名不同的话)。
# ---------------------------------------------------------------------------
OWNER="${DEEPX_OWNER:-itmisx}"
REPO="${DEEPX_REPO:-deepx-code}"
BIN_NAME="deepx"

SOURCE="${SOURCE:-${DEEPX_SOURCE:-github}}"
case "$SOURCE" in
    github)
        GIT_HOST="github.com"
        RELEASE_API="https://api.github.com/repos/${OWNER}/${REPO}/releases/latest"
        DL_BASE="https://github.com/${OWNER}/${REPO}/releases/download"
        RELEASE_PAGE="https://github.com/${OWNER}/${REPO}/releases"
        ;;
    gitee)
        # Gitee 兼容 GitHub 的 release 下载 URL 格式;版本查询走 Gitee v5 OpenAPI。
        GIT_HOST="gitee.com"
        RELEASE_API="https://gitee.com/api/v5/repos/${OWNER}/${REPO}/releases/latest"
        DL_BASE="https://gitee.com/${OWNER}/${REPO}/releases/download"
        RELEASE_PAGE="https://gitee.com/${OWNER}/${REPO}/releases"
        ;;
    *)
        echo "Unknown SOURCE: $SOURCE (用 github 或 gitee)" >&2
        exit 1
        ;;
esac

# ---------------------------------------------------------------------------
# 颜色 & 日志
# ---------------------------------------------------------------------------
if [ -t 1 ]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    BLUE='\033[0;34m'
    CYAN='\033[0;36m'
    BOLD='\033[1m'
    RESET='\033[0m'
else
    RED='' GREEN='' YELLOW='' BLUE='' CYAN='' BOLD='' RESET=''
fi

info()    { echo -e "${CYAN}[INFO]${RESET}  $*"; }
success() { echo -e "${GREEN}[OK]${RESET}    $*"; }
warn()    { echo -e "${YELLOW}[WARN]${RESET}  $*"; }
error()   { echo -e "${RED}[ERROR]${RESET} $*" >&2; }
step()    { echo -e "\n${BOLD}${BLUE}==>${RESET}${BOLD} $*${RESET}"; }

# ---------------------------------------------------------------------------
# 参数
# ---------------------------------------------------------------------------
VERSION=""
PREFIX="$HOME/.local"
FROM_SOURCE=false

while [ $# -gt 0 ]; do
    case "$1" in
        --version) VERSION="$2"; shift 2 ;;
        --prefix)  PREFIX="$2";  shift 2 ;;
        --from-source) FROM_SOURCE=true; shift ;;
        --help|-h)
            cat <<EOF
deepx 一键安装

用法:
  $0                          安装最新版到 ~/.local/bin
  $0 --version v0.1.0         指定版本
  $0 --prefix /usr/local      改安装前缀(默认 ~/.local,二进制最终在 \$PREFIX/bin)
  $0 --from-source            本地 go build(需 Go 1.25+),适合 fork/开发者

环境变量:
  DEEPX_VERSION=v0.1.0        等同于 --version
  DEEPX_PREFIX=/opt/deepx     等同于 --prefix
EOF
            exit 0
            ;;
        *) error "Unknown argument: $1"; exit 1 ;;
    esac
done

# env 兜底(curl | bash 场景下没法传参,只能用 env)
VERSION="${VERSION:-${DEEPX_VERSION:-}}"
PREFIX="${PREFIX:-${DEEPX_PREFIX:-$HOME/.local}}"
BIN_DIR="$PREFIX/bin"

# ---------------------------------------------------------------------------
# Banner
# ---------------------------------------------------------------------------
echo ""
echo -e "${BOLD}${CYAN}  ┌─────────────────┐${RESET}"
echo -e "${BOLD}${CYAN}  │     deepx       │${RESET}   终端里的 AI 代码助手"
echo -e "${BOLD}${CYAN}  └─────────────────┘${RESET}   ${GIT_HOST}/${OWNER}/${REPO}"
echo ""

# ---------------------------------------------------------------------------
# Step 1: 检测平台
# ---------------------------------------------------------------------------
step "Detecting platform"

OS=""
case "$(uname -s)" in
    Darwin) OS="darwin" ;;
    Linux)  OS="linux"  ;;
    MINGW*|MSYS*|CYGWIN*) OS="windows" ;;
    *) error "Unsupported OS: $(uname -s)"; exit 1 ;;
esac

ARCH=""
case "$(uname -m)" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) error "Unsupported arch: $(uname -m)"; exit 1 ;;
esac

# 兼容性:Windows arm64 当前不出包(goreleaser 里 ignored),硬塞会 404
if [ "$OS" = "windows" ] && [ "$ARCH" = "arm64" ]; then
    error "Windows arm64 暂未发布预编译二进制。请用 --from-source 自行编译。"
    exit 1
fi

success "Platform: ${OS}/${ARCH}"

# ---------------------------------------------------------------------------
# Step 1.5: Alpine / musl 兼容层
# 预编译 linux 二进制是 glibc 动态链接 —— OCR 经 purego 在运行时 dlopen,链接器据此产出
# 动态可执行(即便 CGO_ENABLED=0 也非纯静态)。Alpine 用 musl,缺 glibc 解释器
# /lib64/ld-linux-*.so → 装好也跑不起来(见 issue #99)。装 gcompat + libc6-compat
# 提供 glibc 兼容层即可正常运行(已实测)。
# ---------------------------------------------------------------------------
if [ "$OS" = "linux" ] && { [ -f /etc/alpine-release ] || ldd --version 2>&1 | grep -qi musl; }; then
    warn "检测到 musl libc(Alpine 等):deepx 预编译二进制为 glibc 动态链接,需 glibc 兼容层才能运行。"
    if command -v apk &>/dev/null; then
        info "安装兼容层:apk add gcompat libc6-compat"
        if apk add --no-cache gcompat libc6-compat >/dev/null 2>&1; then
            success "已安装 gcompat + libc6-compat"
        else
            warn "自动安装失败(可能非 root / 无网络)。请手动执行:  apk add gcompat libc6-compat"
        fi
    else
        warn "未找到 apk。请用所在发行版的包管理器安装 glibc 兼容层(Alpine:apk add gcompat libc6-compat)。"
    fi
fi

# ---------------------------------------------------------------------------
# Step 2: 检测必备工具
# ---------------------------------------------------------------------------
step "Checking required tools"

need() {
    if ! command -v "$1" &>/dev/null; then
        error "需要 \`$1\`,请先安装。"
        case "$OS" in
            darwin) info "macOS: brew install $1" ;;
            linux)  info "Linux: sudo apt install -y $1   # 或对应包管理器" ;;
        esac
        exit 1
    fi
}
need curl
need tar

if [ "$FROM_SOURCE" = true ]; then
    need git
    need go
    GO_VER=$(go version | awk '{print $3}' | sed 's/^go//')
    info "Found Go ${GO_VER}"
fi

# ---------------------------------------------------------------------------
# Step 3: 从源码安装(可选分支)
# ---------------------------------------------------------------------------
if [ "$FROM_SOURCE" = true ]; then
    step "Building from source"
    TMPDIR=$(mktemp -d)
    trap 'rm -rf "$TMPDIR"' EXIT
    info "Cloning ${OWNER}/${REPO} into $TMPDIR..."
    git clone --depth=1 "https://${GIT_HOST}/${OWNER}/${REPO}.git" "$TMPDIR/${REPO}"
    info "Running go build..."
    BUILTIN_VER=$(date +%Y%m%d%H%M%S)  # 注入内嵌 skill 版本号,触发安装后自动刷新
    (cd "$TMPDIR/${REPO}" && go build -trimpath -ldflags="-s -w -X deepx/skill.builtinVersion=${BUILTIN_VER}" -o "$BIN_NAME" .)
    mkdir -p "$BIN_DIR"
    install -m 0755 "$TMPDIR/${REPO}/${BIN_NAME}" "$BIN_DIR/${BIN_NAME}"
    success "Built and installed: $BIN_DIR/$BIN_NAME"
else
    # ---------------------------------------------------------------------------
    # Step 3b: 解析版本(--version 优先,否则查 latest)
    # ---------------------------------------------------------------------------
    step "Resolving version"
    if [ -z "$VERSION" ]; then
        info "Querying latest release from ${SOURCE}..."
        # 先写到临时文件再解析,避免 curl | grep 管道断写导致 (23) 错误
        VERSION_FILE=$(mktemp)
        if ! curl -fsSL --connect-timeout 10 --max-time 15 \
            -H "Accept: application/json" "$RELEASE_API" -o "$VERSION_FILE"; then
            rm -f "$VERSION_FILE"
            error "无法查询最新版本(来源:${SOURCE})。可能是网络问题或 API 限频。"
            info "方案1: 稍后重试"
            info "方案2: 用 --version v0.x.y 显式指定版本(跳过查询)"
            info "方案3: 切换来源重试(SOURCE=gitee 或 SOURCE=github)"
            exit 1
        fi
        VERSION=$(grep -m1 '"tag_name"' "$VERSION_FILE" | sed -E 's/.*"tag_name":[[:space:]]*"([^"]+)".*/\1/')
        rm -f "$VERSION_FILE"
        if [ -z "$VERSION" ]; then
            error "无法解析最新版本。请用 --version v0.1.0 显式指定。"
            exit 1
        fi
    fi
    success "Version: ${VERSION}"

    # ---------------------------------------------------------------------------
    # Step 4: 下载 + 解压
    # ---------------------------------------------------------------------------
    step "Downloading release asset"

    # goreleaser v2 的 {{.Version}} 不含 v 前缀,产物名 e.g. deepx_0.1.0_darwin_arm64.tar.gz
    # URL 路径里的 tag 仍保留 v 前缀。
    # 注意:asset 前缀来自 goreleaser 的 project_name(deepx),不是 GitHub 仓库名(deepx-code)。
    VERSION_NO_V="${VERSION#v}"
    EXT="tar.gz"
    [ "$OS" = "windows" ] && EXT="zip"
    ASSET="${BIN_NAME}_${VERSION_NO_V}_${OS}_${ARCH}.${EXT}"
    URL="${DL_BASE}/${VERSION}/${ASSET}"

    TMPDIR=$(mktemp -d)
    trap 'rm -rf "$TMPDIR"' EXIT
    info "URL: ${URL}"
    # --progress-bar:下载大文件时显示进度条(去掉 -s 的静默,保留 -S 让出错仍报错)
    if ! curl -fSL --progress-bar --connect-timeout 10 --max-time 120 "$URL" -o "$TMPDIR/$ASSET"; then
        error "下载失败。常见原因:版本号不存在,或该平台没出包。"
        info "可在浏览器查看可用资产:${RELEASE_PAGE}/tag/${VERSION}"
        exit 1
    fi
    success "Downloaded: $(du -h "$TMPDIR/$ASSET" | awk '{print $1}')"

    # 校验和(可选,有的话比对)
    if curl -fsSL "${DL_BASE}/${VERSION}/checksums.txt" \
            -o "$TMPDIR/checksums.txt" 2>/dev/null; then
        info "Verifying checksum..."
        EXPECTED=$(grep " ${ASSET}\$" "$TMPDIR/checksums.txt" | awk '{print $1}')
        if [ -n "$EXPECTED" ]; then
            if command -v sha256sum &>/dev/null; then
                ACTUAL=$(sha256sum "$TMPDIR/$ASSET" | awk '{print $1}')
            elif command -v shasum &>/dev/null; then
                ACTUAL=$(shasum -a 256 "$TMPDIR/$ASSET" | awk '{print $1}')
            else
                warn "未找到 sha256sum/shasum,跳过校验"
                ACTUAL="$EXPECTED"
            fi
            if [ "$ACTUAL" != "$EXPECTED" ]; then
                error "校验失败: 期望 $EXPECTED, 实际 $ACTUAL"
                exit 1
            fi
            success "Checksum OK"
        else
            warn "checksums.txt 里没找到 ${ASSET},跳过校验"
        fi
    else
        warn "未拉到 checksums.txt,跳过校验"
    fi

    info "Extracting..."
    if [ "$EXT" = "tar.gz" ]; then
        tar -xzf "$TMPDIR/$ASSET" -C "$TMPDIR"
    else
        need unzip
        unzip -q "$TMPDIR/$ASSET" -d "$TMPDIR"
    fi

    # 找解压出的二进制(goreleaser 的 archive 把二进制放在根)
    BIN_SRC="$TMPDIR/$BIN_NAME"
    [ "$OS" = "windows" ] && BIN_SRC="$TMPDIR/${BIN_NAME}.exe"
    if [ ! -f "$BIN_SRC" ]; then
        # 兜底:递归找一下
        BIN_SRC=$(find "$TMPDIR" -maxdepth 2 -type f -name "${BIN_NAME}*" | head -1)
    fi
    if [ -z "$BIN_SRC" ] || [ ! -f "$BIN_SRC" ]; then
        error "解压后未找到 ${BIN_NAME} 可执行文件"
        exit 1
    fi

    # ---------------------------------------------------------------------------
    # Step 5: 安装到目标目录
    # ---------------------------------------------------------------------------
    step "Installing to ${BIN_DIR}"

    mkdir -p "$BIN_DIR"
    # 备份旧二进制(如果有),便于回滚
    if [ -f "$BIN_DIR/$BIN_NAME" ]; then
        info "备份现有 ${BIN_NAME} 到 ${BIN_DIR}/${BIN_NAME}.bak"
        mv -f "$BIN_DIR/$BIN_NAME" "$BIN_DIR/${BIN_NAME}.bak"
    fi
    install -m 0755 "$BIN_SRC" "$BIN_DIR/$BIN_NAME"
    success "Installed: $BIN_DIR/$BIN_NAME"
fi

# ---------------------------------------------------------------------------
# Step 5.5: 记录安装来源
# `deepx upgrade` / 应用内升级检查据此决定回 GitHub 还是 Gitee(查版本 API + 重跑哪个源的脚本)。
# 用独立的纯文本标记文件,免去在 shell 里合并 meta.json(没 jq 也能 echo 一行)。
# ---------------------------------------------------------------------------
mkdir -p "$HOME/.deepx"
printf '%s\n' "$SOURCE" > "$HOME/.deepx/.upgrade_source"

# ---------------------------------------------------------------------------
# Step 6: PATH 配置 (改 shell rc)
# ---------------------------------------------------------------------------
step "Setting up shell PATH"

# 已经在 PATH 里就跳
if echo ":$PATH:" | grep -q ":$BIN_DIR:"; then
    success "${BIN_DIR} 已在 PATH"
else
    # 选 rc 文件:优先 SHELL 当前,fallback 都加一份
    LINE='export PATH="'"$BIN_DIR"':$PATH"'
    append_rc() {
        local rc="$1"
        # 注意:必须 return 0。裸 return 会带回上一条命令([ -f ])的退出码,
        # 文件不存在时即 1,在 set -e 下会让整个安装在第一个不存在的 rc(常见是 .zshrc)处中止,
        # 导致 PATH 从未写入、deepx 装了却不在 PATH。Mac 默认有 .zshrc 故不暴露,Linux 必中。
        [ -f "$rc" ] || return 0
        if grep -Fq "$BIN_DIR" "$rc"; then
            info "PATH 已在 $(basename "$rc") 配过"
            return 0
        fi
        printf "\n# deepx\n%s\n" "$LINE" >> "$rc"
        success "已加入 $(basename "$rc")"
    }
    append_rc "$HOME/.zshrc"
    append_rc "$HOME/.bashrc"
    append_rc "$HOME/.bash_profile"
    # Linux 桌面环境(sddm/gdm/lightdm)通常走 .profile 而非 .bashrc
    append_rc "$HOME/.profile"

    # fish:不同语法
    FISH_CFG="$HOME/.config/fish/config.fish"
    if [ -f "$FISH_CFG" ]; then
        if grep -Fq "$BIN_DIR" "$FISH_CFG"; then
            info "PATH 已在 config.fish 配过"
        else
            printf "\n# deepx\nfish_add_path %s\n" "$BIN_DIR" >> "$FISH_CFG"
            success "已加入 config.fish"
        fi
    fi
fi

# 使当前 shell 立即可用(不等 source)
export PATH="$BIN_DIR:$PATH"

# ---------------------------------------------------------------------------
# Step 7: 验证
# ---------------------------------------------------------------------------
step "Verifying"

if [ -x "$BIN_DIR/$BIN_NAME" ]; then
    success "$($BIN_DIR/$BIN_NAME --version 2>/dev/null || echo "$BIN_NAME 可执行 (--version 未实现)")"
else
    error "$BIN_DIR/$BIN_NAME 不可执行"
    exit 1
fi

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
echo ""
echo -e "${BOLD}${GREEN}deepx 安装完成!${RESET}"
echo ""
echo "  下一步:"
echo "    ⚠ 当前 shell 还没拿到新 PATH(子进程改不了父进程环境)。任选其一让它生效:"
echo "         exec \$SHELL                # 推荐:换个新 shell 替代当前(README 推荐的装命令已自动做这件事)"
echo "         source ~/.bashrc           # 或:bash 用户重读 rc"
echo "         source ~/.zshrc            # 或:zsh 用户重读 rc"
echo "         source ~/.config/fish/config.fish   # 或:fish"
echo ""
echo "    新开的 terminal 自动生效,不用做任何事。"
echo ""
echo "    首次启动会引导你配置 API key(写入 ~/.deepx/model.yaml)"
echo ""
echo "  卸载: rm -f ${BIN_DIR}/${BIN_NAME} && rm -rf ~/.deepx"
echo ""
