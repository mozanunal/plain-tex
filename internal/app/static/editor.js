// Editor page script. It runs from a static file so the Content-Security-Policy
// can forbid inline scripts; the server passes per-page values through the
// #editor-config JSON block that editor.html renders.
    const editorConfig = JSON.parse(document.getElementById('editor-config').textContent);

    pdfjsLib.GlobalWorkerOptions.workerSrc = '/static/vendor/pdfjs-dist-3.11.174/pdf.worker.min.js';

    const projectId = editorConfig.projectId;
    const canWrite = editorConfig.canWrite;
    const canComment = editorConfig.canComment;
    const canManageMembers = editorConfig.canManageMembers;
    const initialStatusMessage = editorConfig.status;
    const initialErrorMessage = editorConfig.error;
    const initialContent = editorConfig.content;
    const initialCompileTarget = editorConfig.compileTarget;
    const MIN_PDF_ZOOM = 0.4;
    const MAX_PDF_ZOOM = 3;
    const MAX_COMMENT_BODY_LENGTH = 2000;
    const DEFAULT_PRIMARY_FILE = 'main.tex';
    const SIDEBAR_PANEL_STORAGE_KEY = 'polytxt.sidebarPanel';
    const SIDEBAR_COLLAPSED_STORAGE_KEY = 'polytxt.sidebarCollapsed';
    const WORKSPACE_VIEW_STORAGE_KEY = 'polytxt.workspaceView';
    const WORKSPACE_VIEW_EDITOR = 'editor';
    const WORKSPACE_VIEW_PDF = 'pdf';
    const WORKSPACE_VIEW_SPLIT = 'split';
    const initialPrimaryFile = initialCompileTarget || DEFAULT_PRIMARY_FILE;
    let editor;
    let currentFile = initialPrimaryFile;
    let currentFileIsText = true;
    let currentPreviewURL = null;
    let fileList = [];
    let filesByPath = {};
    let fileContents = {};
    let unsavedChanges = {};
    let expandedFolders = new Set(['']);
    let dragSourcePath = null;
    let dragPreviewElement = null;
    let pdfDocument = null;
    let pdfZoom = 1;
    let pdfRenderToken = 0;
    let pageTextMap = new Map();
    let lineToPageMap = new Map();
    let pageToLinesMap = new Map();
    let activePDFPage = null;
    let lineHighlightDecorations = [];
    let lineHighlightTimer = null;
    let commentDecorations = [];
    let suppressEditorPDFSync = false;
    let suppressEditorChangeTracking = false;
    let pdfResizeTimer = null;
    let comments = [];
    let commentTarget = { filePath: initialPrimaryFile, startLine: 1, endLine: 1, snippet: '' };
    let compileTarget = initialPrimaryFile;
    let activeSidebarPanel = 'files';
    let isSidebarCollapsed = false;
    let workspaceView = WORKSPACE_VIEW_SPLIT;
    let gitStatus = { configured: false, repoPresent: false };

    fileContents[initialPrimaryFile] = initialContent;
    const fileHashes = {};

    require.config({ paths: { vs: '/static/vendor/monaco-editor-0.45.0/vs' } });

    require(['vs/editor/editor.main'], function () {
        monaco.languages.register({ id: 'latex' });
        monaco.languages.setMonarchTokensProvider('latex', {
            tokenizer: {
                root: [
                    [/\\[a-zA-Z]+/, 'keyword'],
                    [/\{/, 'delimiter.curly'],
                    [/\}/, 'delimiter.curly'],
                    [/\[/, 'delimiter.square'],
                    [/\]/, 'delimiter.square'],
                    [/%.*$/, 'comment'],
                    [/\$[^$]*\$/, 'string'],
                ]
            }
        });

        monaco.languages.register({ id: 'bibtex' });
        monaco.languages.setMonarchTokensProvider('bibtex', {
            tokenizer: {
                root: [
                    [/@[a-zA-Z]+/, 'keyword'],
                    [/\{/, 'delimiter.curly'],
                    [/\}/, 'delimiter.curly'],
                    [/"[^"]*"/, 'string'],
                    [/%.*$/, 'comment'],
                ]
            }
        });

        monaco.languages.register({ id: 'typst' });
        monaco.languages.setMonarchTokensProvider('typst', {
            tokenizer: {
                root: [
                    [/\/\/.*$/, 'comment'],
                    [/#(?:let|set|show|import|include|if|else|for|while|return|break|continue)\b/, 'keyword'],
                    [/#[_a-zA-Z][_a-zA-Z0-9-]*/, 'keyword'],
                    [/"([^"\\]|\\.)*"/, 'string'],
                    [/[{}\[\]()]/, 'delimiter.bracket'],
                    [/\b\d+(\.\d+)?\b/, 'number'],
                ]
            }
        });

        editor = monaco.editor.create(document.getElementById('editor-container'), {
            value: initialContent,
            language: 'latex',
            theme: 'vs',
            fontSize: 14,
            glyphMargin: true,
            minimap: { enabled: false },
            wordWrap: 'on',
            automaticLayout: true,
            readOnly: !canWrite
        });

        editor.onDidChangeModelContent(() => {
            if (suppressEditorChangeTracking || !currentFileIsText || !canWrite) {
                return;
            }
            fileContents[currentFile] = editor.getValue();
            unsavedChanges[currentFile] = true;
            updateFileList();
        });

        editor.onDidChangeCursorPosition((e) => {
            if (suppressEditorPDFSync || !pdfDocument || currentFile !== compileTarget) {
                return;
            }
            if (e.source !== 'mouse') {
                return;
            }
            showLineInPDF(e.position.lineNumber);
        });

        editor.onDidChangeCursorSelection(() => {
            if (!currentFileIsText || !canComment) {
                return;
            }
            updateCommentTargetFromEditor(false);
        });

        if (canWrite) {
            editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, function () {
                save();
            });

            editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.Enter, function () {
                compile();
            });
        }

        editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyMod.Shift | monaco.KeyCode.Enter, function () {
            showCurrentLineInPDF();
        });

        loadFileList();
        loadComments();
        loadGitStatus(false);
        updateCommentTargetFromEditor(true);
    });

    function saveBody(filename, content) {
        let body = `content=${encodeURIComponent(content)}`;
        if (fileHashes[filename]) {
            body += `&baseHash=${encodeURIComponent(fileHashes[filename])}`;
        }
        return body;
    }

    function buildFileURL(filename) {
        const encoded = filename.split('/').map(encodeURIComponent).join('/');
        return `/api/projects/${projectId}/files/${encoded}`;
    }

    function getFileExtension(filename) {
        const dotIndex = filename.lastIndexOf('.');
        if (dotIndex === -1) {
            return '';
        }
        return filename.slice(dotIndex).toLowerCase();
    }

    function isCompilableFile(filename) {
        const ext = getFileExtension(filename);
        return ext === '.tex' || ext === '.typ' || ext === '.md';
    }

    function getLanguageForFile(filename) {
        const ext = getFileExtension(filename);
        if (ext === '.tex' || ext === '.ltx' || ext === '.sty' || ext === '.cls') return 'latex';
        if (ext === '.typ') return 'typst';
        if (ext === '.bib' || ext === '.bst') return 'bibtex';
        if (ext === '.md') return 'markdown';
        return 'plaintext';
    }

    function getParentPath(path) {
        const separatorIndex = path.lastIndexOf('/');
        return separatorIndex === -1 ? '' : path.slice(0, separatorIndex);
    }

    function getBaseName(path) {
        const separatorIndex = path.lastIndexOf('/');
        return separatorIndex === -1 ? path : path.slice(separatorIndex + 1);
    }

    function normalizeClientPath(path) {
        return path
            .split('/')
            .filter(segment => segment && segment !== '.')
            .join('/');
    }

    function ensureFolderExpanded(path) {
        if (!path) {
            expandedFolders.add('');
            return;
        }
        let current = '';
        path.split('/').forEach(segment => {
            if (!segment) return;
            current = current ? `${current}/${segment}` : segment;
            expandedFolders.add(current);
        });
        expandedFolders.add('');
    }

    function normalizeFiles(files) {
        return files
            .map(file => {
                const path = file.path || file.name;
                if (!path) return null;
                return { ...file, path, name: file.name || path };
            })
            .filter(Boolean);
    }

    function buildFileTree(files) {
        const root = { path: '', name: '', isDir: true, children: new Map(), file: null };

        files.forEach(file => {
            const parts = file.path.split('/').filter(Boolean);
            let current = root;
            let currentPath = '';

            parts.forEach((part, index) => {
                currentPath = currentPath ? `${currentPath}/${part}` : part;
                const isLeaf = index === parts.length - 1;
                if (!current.children.has(part)) {
                    current.children.set(part, {
                        path: currentPath,
                        name: part,
                        isDir: !isLeaf || file.isDir,
                        children: new Map(),
                        file: isLeaf ? file : null,
                    });
                }

                const child = current.children.get(part);
                if (isLeaf) {
                    child.isDir = !!file.isDir;
                    child.file = file;
                }
                if (!isLeaf) {
                    child.isDir = true;
                }
                current = child;
            });
        });

        return root;
    }

    function sortedChildren(node) {
        return Array.from(node.children.values()).sort((a, b) => {
            if (a.isDir !== b.isDir) {
                return a.isDir ? -1 : 1;
            }
            return a.name.localeCompare(b.name);
        });
    }

    function canDropPath(sourcePath, targetDirPath) {
        if (!canWrite) {
            return false;
        }

        if (!sourcePath) {
            return false;
        }

        const source = filesByPath[sourcePath];
        if (!source) {
            return false;
        }

        if (targetDirPath) {
            const target = filesByPath[targetDirPath];
            if (!target || !target.isDir) {
                return false;
            }
        }

        if (source.isDir && targetDirPath && (targetDirPath === sourcePath || targetDirPath.startsWith(`${sourcePath}/`))) {
            return false;
        }

        const destinationPath = targetDirPath ? `${targetDirPath}/${getBaseName(sourcePath)}` : getBaseName(sourcePath);
        if (destinationPath === sourcePath) {
            return false;
        }
        if (Object.prototype.hasOwnProperty.call(filesByPath, destinationPath)) {
            return false;
        }

        return true;
    }

    function clearDropHighlights() {
        document.querySelectorAll('.file-tree-drop-target').forEach(el => el.classList.remove('file-tree-drop-target'));
        document.querySelectorAll('.file-tree-drop-target-root').forEach(el => el.classList.remove('file-tree-drop-target-root'));
    }

    function attachDropTarget(element, targetDirPath, isRoot = false) {
        element.addEventListener('dragover', (event) => {
            const sourcePath = dragSourcePath || event.dataTransfer.getData('text/plain');
            if (!canDropPath(sourcePath, targetDirPath)) {
                return;
            }

            event.preventDefault();
            event.dataTransfer.dropEffect = 'move';
            clearDropHighlights();
            element.classList.add(isRoot ? 'file-tree-drop-target-root' : 'file-tree-drop-target');
        });

        element.addEventListener('dragleave', () => {
            element.classList.remove('file-tree-drop-target');
            element.classList.remove('file-tree-drop-target-root');
        });

        element.addEventListener('drop', async (event) => {
            event.preventDefault();
            clearDropHighlights();
            const sourcePath = dragSourcePath || event.dataTransfer.getData('text/plain');
            if (!canDropPath(sourcePath, targetDirPath)) {
                return;
            }
            await movePath(sourcePath, targetDirPath);
        });
    }

    function remapPathState(previousPath, nextPath) {
        const pathPrefix = `${previousPath}/`;

        Object.keys(fileContents).forEach(path => {
            if (path === previousPath || path.startsWith(pathPrefix)) {
                const suffix = path.slice(previousPath.length);
                const mappedPath = `${nextPath}${suffix}`;
                fileContents[mappedPath] = fileContents[path];
                delete fileContents[path];
            }
        });

        Object.keys(unsavedChanges).forEach(path => {
            if (path === previousPath || path.startsWith(pathPrefix)) {
                const suffix = path.slice(previousPath.length);
                const mappedPath = `${nextPath}${suffix}`;
                unsavedChanges[mappedPath] = true;
                delete unsavedChanges[path];
            }
        });

        if (currentFile === previousPath || currentFile.startsWith(pathPrefix)) {
            const suffix = currentFile.slice(previousPath.length);
            currentFile = `${nextPath}${suffix}`;
            document.getElementById('current-file').textContent = currentFile;
        }

        const updatedExpanded = new Set();
        expandedFolders.forEach(path => {
            if (path === previousPath || path.startsWith(pathPrefix)) {
                const suffix = path.slice(previousPath.length);
                updatedExpanded.add(`${nextPath}${suffix}`);
            } else {
                updatedExpanded.add(path);
            }
        });
        expandedFolders = updatedExpanded;
        ensureFolderExpanded(getParentPath(currentFile));
        ensureFolderExpanded(getParentPath(nextPath));
    }

    async function movePath(sourcePath, targetDirPath) {
        if (!canWrite) {
            setStatus('Read-only access', true);
            return;
        }

        if (!canDropPath(sourcePath, targetDirPath)) {
            return;
        }

        const targetPath = targetDirPath ? `${targetDirPath}/${getBaseName(sourcePath)}` : getBaseName(sourcePath);
        setStatus(`Moving ${sourcePath}...`);

        try {
            const response = await fetch(`/api/projects/${projectId}/files/move`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
                body: `source=${encodeURIComponent(sourcePath)}&targetDir=${encodeURIComponent(targetDirPath || '')}`,
            });

            if (!response.ok) {
                setStatus('Failed to move item', true);
                return;
            }

            let nextPath = targetPath;
            try {
                const payload = await response.json();
                if (payload && payload.path) {
                    nextPath = payload.path;
                }
            } catch (_) {}

            remapPathState(sourcePath, nextPath);
            await loadFileList();
            await loadComments();

            if (!filesByPath[currentFile] || filesByPath[currentFile].isDir) {
                const fallbackFile = getDefaultOpenFile();
                if (fallbackFile) {
                    await openFile(fallbackFile);
                }
            } else {
                await openFile(currentFile);
            }

            setStatus(`Moved to ${nextPath}`);
        } catch (_) {
            setStatus('Failed to move item', true);
        }
    }

    function renderTreeNode(container, node, depth) {
        const row = document.createElement('div');
        row.className = 'file-tree-row flex items-center gap-1 rounded py-1 pr-1';
        row.style.paddingLeft = `${8 + depth * 14}px`;

        if (node.path === currentFile) {
            row.classList.add('bg-gray-600');
        } else {
            row.classList.add('hover:bg-gray-700');
        }
        if (node.path === dragSourcePath) {
            row.classList.add('drag-source');
        }

        const toggle = document.createElement('button');
        toggle.type = 'button';
        toggle.className = 'w-4 text-xs text-gray-400';
        toggle.textContent = node.isDir ? (expandedFolders.has(node.path) ? 'v' : '>') : '';
        if (node.isDir) {
            toggle.onclick = (event) => {
                event.stopPropagation();
                if (expandedFolders.has(node.path)) {
                    expandedFolders.delete(node.path);
                } else {
                    expandedFolders.add(node.path);
                }
                renderFileList(fileList);
            };
        } else {
            toggle.disabled = true;
        }
        row.appendChild(toggle);

        const label = document.createElement('span');
        label.className = 'text-sm truncate flex-1';
        if (node.isDir) {
            label.className += ' text-gray-300';
            label.textContent = `${node.name}/`;
            label.onclick = () => {
                if (expandedFolders.has(node.path)) {
                    expandedFolders.delete(node.path);
                } else {
                    expandedFolders.add(node.path);
                }
                renderFileList(fileList);
            };
            attachDropTarget(row, node.path);
        } else {
            label.textContent = node.name + (unsavedChanges[node.path] ? ' *' : '');
            label.onclick = () => openFile(node.path);
            if (!node.file.isText) {
                label.className += ' text-blue-200';
                label.title = 'Preview-only file';
            }
        }
        row.appendChild(label);

        if (!node.isDir && isCompilableFile(node.path)) {
            const isEntry = node.path === compileTarget;
            const entryBtn = document.createElement('button');
            entryBtn.type = 'button';

            if (isEntry) {
                // Always visible, so the entry point is identifiable at a glance.
                entryBtn.className = 'text-amber-400 text-base p-0 cursor-default';
                entryBtn.textContent = '◆';
                entryBtn.title = 'Compile entry point';
                entryBtn.disabled = true;
            } else if (canWrite) {
                entryBtn.className = 'delete-btn text-gray-400 hover:text-amber-400 text-base p-0';
                entryBtn.textContent = '◇';
                entryBtn.title = `Set ${node.name} as the compile entry point`;
                entryBtn.onclick = (event) => {
                    event.stopPropagation();
                    void setCompileEntry(node.path);
                };
            }

            if (isEntry || canWrite) {
                row.appendChild(entryBtn);
            }
        }

        const copyPathBtn = document.createElement('button');
        copyPathBtn.type = 'button';
        copyPathBtn.className = 'delete-btn text-gray-400 hover:text-gray-200 text-base p-0';
        copyPathBtn.textContent = '⎘';
        copyPathBtn.title = 'Copy path';
        copyPathBtn.onclick = (event) => {
            event.stopPropagation();
            navigator.clipboard.writeText(node.path).then(() => {
                setStatus(`Copied: ${node.path}`);
            }).catch(() => {
                setStatus('Failed to copy path', true);
            });
        };
        row.appendChild(copyPathBtn);

        if (canWrite) {
            const renameBtn = document.createElement('button');
            renameBtn.type = 'button';
            renameBtn.className = 'delete-btn text-blue-400 hover:text-blue-300 text-base p-0';
            renameBtn.textContent = '✎';
            renameBtn.title = 'Rename';
            renameBtn.onclick = (event) => {
                event.stopPropagation();
                openRenameModal(node.path);
            };
            row.appendChild(renameBtn);

            const deleteBtn = document.createElement('button');
            deleteBtn.type = 'button';
            deleteBtn.className = 'delete-btn text-red-400 hover:text-red-300 text-base p-0';
            deleteBtn.textContent = '✕';
            deleteBtn.onclick = (event) => {
                event.stopPropagation();
                deleteFile(node.path);
            };
            row.appendChild(deleteBtn);
        }

        if (canWrite) {
            row.draggable = true;
            row.addEventListener('dragstart', (event) => {
                dragSourcePath = node.path;
                dragPreviewElement = row;
                row.classList.add('drag-source');
                event.dataTransfer.effectAllowed = 'move';
                event.dataTransfer.setData('text/plain', node.path);
            });
            row.addEventListener('dragend', () => {
                dragSourcePath = null;
                if (dragPreviewElement) {
                    dragPreviewElement.classList.remove('drag-source');
                    dragPreviewElement = null;
                }
                clearDropHighlights();
            });
        }

        container.appendChild(row);

        if (node.isDir && expandedFolders.has(node.path)) {
            sortedChildren(node).forEach(child => renderTreeNode(container, child, depth + 1));
        }
    }

    function renderFileList(files) {
        const container = document.getElementById('file-list');
        container.innerHTML = '';

        const rootDropZone = document.createElement('div');
        rootDropZone.className = 'rounded border border-dashed border-gray-600 px-2 py-1 text-[11px] text-gray-400';
        rootDropZone.textContent = 'Drop here to move item to project root';
        attachDropTarget(rootDropZone, '', true);
        container.appendChild(rootDropZone);

        const tree = buildFileTree(files);
        sortedChildren(tree).forEach(node => renderTreeNode(container, node, 0));
    }

    function updateFileList() {
        renderFileList(fileList);
    }

    async function persistCompileTargetSelection(entry, showSuccess = false) {
        if (!canWrite) {
            return true;
        }

        try {
            const formData = new FormData();
            formData.append('entry', entry || '');
            const response = await fetch(`/api/projects/${projectId}/compile-target`, {
                method: 'POST',
                body: formData,
            });
            if (!response.ok) {
                const message = (await response.text()).trim() || 'Failed to save the entry point';
                setStatus(message, true);
                return false;
            }
            if (showSuccess) {
                setStatus(`Entry point set to ${entry}`);
            }
            return true;
        } catch (_) {
            setStatus('Failed to save the entry point', true);
            return false;
        }
    }

    function listCompileTargets() {
        return fileList
            .filter(file => !file.isDir && isCompilableFile(file.path))
            .map(file => file.path)
            .sort((a, b) => a.localeCompare(b));
    }

    function pickCompileTarget(candidates) {
        if (compileTarget && candidates.includes(compileTarget)) {
            return compileTarget;
        }
        if (currentFile && candidates.includes(currentFile)) {
            return currentFile;
        }
        if (candidates.includes('main.tex')) {
            return 'main.tex';
        }
        if (candidates.includes('main.typ')) {
            return 'main.typ';
        }
        if (candidates.includes('main.md')) {
            return 'main.md';
        }
        return candidates[0] || '';
    }

    function getDefaultOpenFile() {
        if (compileTarget && filesByPath[compileTarget] && !filesByPath[compileTarget].isDir) {
            return compileTarget;
        }
        const firstTextFile = fileList.find(file => !file.isDir && file.isText);
        if (firstTextFile) {
            return firstTextFile.path;
        }
        const firstFile = fileList.find(file => !file.isDir);
        return firstFile ? firstFile.path : '';
    }

    // The entry point lives in the file browser now. This keeps the in-memory
    // value consistent with what is actually on disk, and clears the stored
    // value when the last compilable file disappears.
    function refreshCompileTarget() {
        const candidates = listCompileTargets();

        if (!candidates.length) {
            const previousTarget = compileTarget;
            compileTarget = '';
            if (previousTarget) {
                void persistCompileTargetSelection('', false);
            }
            updateEntryDisplay();
            return;
        }

        compileTarget = pickCompileTarget(candidates);
        updateEntryDisplay();
    }

    function updateEntryDisplay() {
        const display = document.getElementById('settings-entry-display');
        if (display) {
            display.textContent = compileTarget || 'No .tex/.typ/.md files';
        }
    }

    // Persists to the project row so the choice survives a reload.
    async function setCompileEntry(path) {
        if (!canWrite || path === compileTarget) return;

        const previousTarget = compileTarget;
        compileTarget = path;
        renderFileList(fileList);
        updateEntryDisplay();

        if (!await persistCompileTargetSelection(path, true)) {
            compileTarget = previousTarget;
            renderFileList(fileList);
            updateEntryDisplay();
        }
    }

    async function loadFileList() {
        try {
            const response = await fetch(`/api/projects/${projectId}/files`);
            if (!response.ok) {
                setStatus('Failed to load files', true);
                return;
            }

            fileList = normalizeFiles(await response.json());
            filesByPath = {};
            fileList.forEach(file => {
                filesByPath[file.path] = file;
            });

            refreshCompileTarget();

            const currentFileEntry = filesByPath[currentFile];
            if (currentFileEntry && !currentFileEntry.isDir) {
                ensureFolderExpanded(getParentPath(currentFile));
                renderFileList(fileList);
                return;
            }

            const fallbackFile = getDefaultOpenFile();
            if (!fallbackFile) {
                currentFile = '';
                currentFileIsText = true;
                document.getElementById('current-file').textContent = '(no file)';
                renderFileList(fileList);
                suppressEditorChangeTracking = true;
                editor.setValue('');
                suppressEditorChangeTracking = false;
                showEditor();
                setSaveEnabled(false);
                return;
            }

            ensureFolderExpanded(getParentPath(fallbackFile));
            renderFileList(fileList);
            await openFile(fallbackFile);
        } catch (_) {
            setStatus('Failed to load files', true);
        }
    }

    async function openFile(filename) {
        const file = filesByPath[filename];
        if (!file || file.isDir) return;
        if (filename === currentFile && file.isText === currentFileIsText) {
            renderCommentList();
            renderCommentDecorations();
            return;
        }

        currentFile = filename;
        currentFileIsText = file.isText;
        ensureFolderExpanded(getParentPath(filename));
        document.getElementById('current-file').textContent = filename;
        updateFileList();
        renderCommentList();

        if (file.isText) {
            await openTextFile(filename);
        } else {
            await openBinaryFile(filename, file.contentType || '');
        }
    }

    async function openTextFile(filename) {
        showEditor();
        setSaveEnabled(canWrite);

        const loaded = await ensureTextFileCached(filename);
        if (!loaded) {
            setStatus('Failed to load file', true);
            return;
        }

        const model = editor.getModel();
        monaco.editor.setModelLanguage(model, getLanguageForFile(filename));
        suppressEditorChangeTracking = true;
        editor.setValue(fileContents[filename] || '');
        suppressEditorChangeTracking = false;
        editor.layout();
        editor.focus();
        updateCommentTargetFromEditor(true);
        renderCommentDecorations();
        renderCommentList();
    }

    async function ensureTextFileCached(filename) {
        if (Object.prototype.hasOwnProperty.call(fileContents, filename)) {
            return true;
        }

        try {
            const response = await fetch(buildFileURL(filename));
            if (!response.ok) {
                return false;
            }
            fileContents[filename] = await response.text();
            const etag = response.headers.get('ETag');
            if (etag) { fileHashes[filename] = etag; }
            return true;
        } catch (_) {
            return false;
        }
    }

    async function openBinaryFile(filename, hintedContentType = '') {
        showPreview();
        setSaveEnabled(false);
        updateCommentTargetLabel();
        renderCommentDecorations();

        const preview = document.getElementById('file-preview');
        preview.innerHTML = '<div class="h-full flex items-center justify-center text-gray-500">Loading preview...</div>';

        try {
            const response = await fetch(buildFileURL(filename));
            if (!response.ok) {
                preview.innerHTML = '<div class="h-full flex items-center justify-center text-red-500">Failed to load preview.</div>';
                setStatus('Failed to load file', true);
                return;
            }

            const blob = await response.blob();
            const contentType = resolveContentType(
                filename,
                response.headers.get('Content-Type') || hintedContentType || blob.type,
            );
            await renderBinaryPreview(blob, filename, contentType);
            setStatus(`Previewing ${filename}`);
        } catch (e) {
            preview.innerHTML = '<div class="h-full flex items-center justify-center text-red-500">Failed to load preview.</div>';
            setStatus('Failed to load file', true);
        }
    }

    function showEditor() {
        clearFilePreview();
        document.getElementById('file-preview').classList.add('hidden');
        document.getElementById('editor-container').classList.remove('hidden');
    }

    function showPreview() {
        document.getElementById('editor-container').classList.add('hidden');
        document.getElementById('file-preview').classList.remove('hidden');
    }

    function setSaveEnabled(enabled) {
        const saveBtn = document.getElementById('save-btn');
        if (!canWrite) {
            enabled = false;
        }
        if (enabled) {
            saveBtn.disabled = false;
            saveBtn.className = 'p-1.5 rounded text-gray-500 hover:text-gray-700 hover:bg-gray-100 transition';
        } else {
            saveBtn.disabled = true;
            saveBtn.className = 'p-1.5 rounded text-gray-300 cursor-not-allowed';
        }
    }

    function readStoredSidebarPanel() {
        try {
            const value = localStorage.getItem(SIDEBAR_PANEL_STORAGE_KEY);
            if (value === 'files' || value === 'comments' || value === 'git' || value === 'settings') {
                return value;
            }
        } catch (_) {}
        return 'files';
    }

    function persistSidebarPanel(panel) {
        try {
            localStorage.setItem(SIDEBAR_PANEL_STORAGE_KEY, panel);
        } catch (_) {}
    }

    function readStoredSidebarCollapsed() {
        try {
            return localStorage.getItem(SIDEBAR_COLLAPSED_STORAGE_KEY) === '1';
        } catch (_) {
            return false;
        }
    }

    function persistSidebarCollapsed(collapsed) {
        try {
            localStorage.setItem(SIDEBAR_COLLAPSED_STORAGE_KEY, collapsed ? '1' : '0');
        } catch (_) {}
    }

    function readStoredWorkspaceView() {
        try {
            const value = localStorage.getItem(WORKSPACE_VIEW_STORAGE_KEY);
            if (value === WORKSPACE_VIEW_EDITOR || value === WORKSPACE_VIEW_PDF || value === WORKSPACE_VIEW_SPLIT) {
                return value;
            }
        } catch (_) {}
        return WORKSPACE_VIEW_SPLIT;
    }

    function persistWorkspaceView(view) {
        try {
            localStorage.setItem(WORKSPACE_VIEW_STORAGE_KEY, view);
        } catch (_) {}
    }

    function setSidebarButtonState(button, isActive) {
        if (!button) {
            return;
        }
        button.className = isActive
            ? 'h-9 w-9 rounded bg-blue-600 flex items-center justify-center text-white hover:bg-blue-500'
            : 'h-9 w-9 rounded flex items-center justify-center text-gray-200 hover:bg-gray-700';
        button.setAttribute('aria-pressed', isActive ? 'true' : 'false');
    }

    function setWorkspaceViewButtonState(button, isActive) {
        if (!button) {
            return;
        }
        button.className = isActive
            ? 'rounded bg-white shadow-sm px-3 py-1 text-xs font-medium text-gray-800'
            : 'rounded px-3 py-1 text-xs text-gray-600 hover:bg-white hover:shadow-sm transition';
        button.setAttribute('aria-pressed', isActive ? 'true' : 'false');
    }

    function applyWorkspaceView(view, persist = true) {
        const normalized = (view === WORKSPACE_VIEW_EDITOR || view === WORKSPACE_VIEW_PDF || view === WORKSPACE_VIEW_SPLIT)
            ? view
            : WORKSPACE_VIEW_SPLIT;
        workspaceView = normalized;

        const layout = document.getElementById('workspace-layout');
        layout.dataset.view = normalized;

        setWorkspaceViewButtonState(document.getElementById('view-editor-btn'), normalized === WORKSPACE_VIEW_EDITOR);
        setWorkspaceViewButtonState(document.getElementById('view-pdf-btn'), normalized === WORKSPACE_VIEW_PDF);
        setWorkspaceViewButtonState(document.getElementById('view-split-btn'), normalized === WORKSPACE_VIEW_SPLIT);

        if (persist) {
            persistWorkspaceView(normalized);
        }

        if (editor && normalized !== WORKSPACE_VIEW_PDF) {
            editor.layout();
        }
        if (pdfDocument && normalized !== WORKSPACE_VIEW_EDITOR) {
            void renderPDFPages();
        }
    }

    function applySidebarPanel(panel, persist = true) {
        const normalized = (panel === 'files' || panel === 'comments' || panel === 'git' || panel === 'settings') ? panel : 'files';
        activeSidebarPanel = normalized;

        document.getElementById('sidebar-panel-files').classList.toggle('hidden', normalized !== 'files');
        document.getElementById('sidebar-panel-comments').classList.toggle('hidden', normalized !== 'comments');
        document.getElementById('sidebar-panel-git').classList.toggle('hidden', normalized !== 'git');
        document.getElementById('sidebar-panel-settings').classList.toggle('hidden', normalized !== 'settings');

        setSidebarButtonState(document.getElementById('sidebar-files-btn'), normalized === 'files');
        setSidebarButtonState(document.getElementById('sidebar-comments-btn'), normalized === 'comments');
        setSidebarButtonState(document.getElementById('sidebar-git-btn'), normalized === 'git');
        setSidebarButtonState(document.getElementById('sidebar-settings-btn'), normalized === 'settings');

        if (persist) {
            persistSidebarPanel(normalized);
        }

        if (editor && workspaceView !== WORKSPACE_VIEW_PDF) {
            editor.layout();
        }
        if (pdfDocument && workspaceView !== WORKSPACE_VIEW_EDITOR) {
            void renderPDFPages();
        }
    }

    function applySidebarCollapsed(collapsed, persist = true) {
        isSidebarCollapsed = Boolean(collapsed);
        const shell = document.getElementById('utility-shell');
        const collapseBtn = document.getElementById('sidebar-collapse-btn');
        const collapseIcon = document.getElementById('sidebar-collapse-icon');

        shell.classList.toggle('utility-collapsed', isSidebarCollapsed);
        collapseIcon.innerHTML = isSidebarCollapsed
            ? '<path fill-rule="evenodd" d="M8.22 5.22a.75.75 0 0 1 1.06 0l4.25 4.25a.75.75 0 0 1 0 1.06l-4.25 4.25a.75.75 0 0 1-1.06-1.06L11.94 10 8.22 6.28a.75.75 0 0 1 0-1.06Z" clip-rule="evenodd" />'
            : '<path fill-rule="evenodd" d="M11.78 5.22a.75.75 0 0 1 0 1.06L8.06 10l3.72 3.72a.75.75 0 1 1-1.06 1.06l-4.25-4.25a.75.75 0 0 1 0-1.06l4.25-4.25a.75.75 0 0 1 1.06 0Z" clip-rule="evenodd" />';
        const label = isSidebarCollapsed ? 'Expand tools panel' : 'Collapse tools panel';
        collapseBtn.title = label;
        collapseBtn.setAttribute('aria-label', label);
        collapseBtn.setAttribute('aria-expanded', String(!isSidebarCollapsed));

        if (persist) {
            persistSidebarCollapsed(isSidebarCollapsed);
        }

        if (editor && workspaceView !== WORKSPACE_VIEW_PDF) {
            editor.layout();
        }
        if (pdfDocument && workspaceView !== WORKSPACE_VIEW_EDITOR) {
            void renderPDFPages();
        }
    }

    function selectSidebarPanel(panel) {
        applySidebarPanel(panel);
        if (isSidebarCollapsed) {
            applySidebarCollapsed(false);
        }
    }

    function toggleSidebarCollapsed() {
        applySidebarCollapsed(!isSidebarCollapsed);
    }

    function buildCommentTargetLabel(target) {
        if (!target || !target.filePath) {
            return 'Target: -';
        }
        const rangeLabel = target.startLine === target.endLine
            ? `${target.startLine}`
            : `${target.startLine}-${target.endLine}`;
        return `Target: ${target.filePath}:${rangeLabel}`;
    }

    function updateCommentTargetLabel() {
        const addBtn = document.getElementById('comment-add-btn');
        const selectionBtn = document.getElementById('comment-use-selection-btn');
        const input = document.getElementById('comment-input');
        const disabled = !canComment || !currentFileIsText;
        addBtn.disabled = disabled;
        selectionBtn.disabled = disabled;
        input.disabled = disabled;
        addBtn.className = disabled
            ? 'w-full rounded bg-gray-300 px-3 py-2 text-sm font-medium text-gray-500 cursor-not-allowed'
            : 'w-full rounded bg-amber-500 px-3 py-2 text-sm font-medium text-white hover:bg-amber-600';
        selectionBtn.className = disabled
            ? 'w-full rounded border border-gray-200 bg-gray-100 px-2 py-1 text-xs text-gray-400 cursor-not-allowed'
            : 'w-full rounded border border-gray-300 bg-gray-50 px-2 py-1 text-xs text-gray-700 hover:bg-gray-100';

        const label = document.getElementById('comment-target');
        if (!canComment) {
            label.textContent = 'Target: your role cannot create comments';
            return;
        }
        if (!currentFileIsText) {
            label.textContent = 'Target: open a text file to comment';
            return;
        }
        label.textContent = buildCommentTargetLabel(commentTarget);
    }

    function clampLineValue(lineValue, min, max) {
        if (!Number.isFinite(lineValue)) {
            return min;
        }
        return Math.min(max, Math.max(min, lineValue));
    }

    function buildCommentSnippet(startLine, endLine) {
        const model = editor ? editor.getModel() : null;
        if (!model) {
            return '';
        }

        const lineCount = model.getLineCount();
        const safeStart = clampLineValue(startLine, 1, lineCount);
        const safeEnd = clampLineValue(endLine, safeStart, lineCount);
        const maxLines = Math.min(4, safeEnd - safeStart + 1);
        const parts = [];
        for (let offset = 0; offset < maxLines; offset++) {
            const line = model.getLineContent(safeStart + offset).trim();
            if (line) {
                parts.push(line);
            }
        }

        const snippet = parts.join(' ').replace(/\s+/g, ' ').trim();
        if (snippet.length <= 220) {
            return snippet;
        }
        return `${snippet.slice(0, 220)}...`;
    }

    function getCommentTargetFromEditor() {
        if (!editor || !currentFileIsText) {
            return null;
        }

        const model = editor.getModel();
        if (!model) {
            return null;
        }

        const lineCount = Math.max(1, model.getLineCount());
        const selection = editor.getSelection();
        const position = editor.getPosition();
        let startLine = position ? position.lineNumber : 1;
        let endLine = startLine;

        if (selection) {
            startLine = selection.startLineNumber;
            endLine = selection.endLineNumber;
            if (!selection.isEmpty() && selection.endColumn === 1 && endLine > startLine) {
                endLine -= 1;
            }
        }

        const safeStart = clampLineValue(startLine, 1, lineCount);
        const safeEnd = clampLineValue(endLine, safeStart, lineCount);
        return {
            filePath: currentFile,
            startLine: safeStart,
            endLine: safeEnd,
            snippet: buildCommentSnippet(safeStart, safeEnd),
        };
    }

    function updateCommentTargetFromEditor(fallbackToCurrentLine = false) {
        const target = getCommentTargetFromEditor();
        if (!target) {
            if (fallbackToCurrentLine) {
                commentTarget = { filePath: currentFile, startLine: 1, endLine: 1, snippet: '' };
                updateCommentTargetLabel();
            }
            return;
        }
        commentTarget = target;
        updateCommentTargetLabel();
    }

    function formatCommentCreatedAt(value) {
        if (!value) {
            return '';
        }
        const iso = value.replace(' ', 'T') + 'Z';
        const parsed = new Date(iso);
        if (Number.isNaN(parsed.getTime())) {
            return value;
        }
        return parsed.toLocaleString();
    }

    function currentCommentScopeIsAllFiles() {
        return document.getElementById('comment-scope-all').checked;
    }

    function visibleComments() {
        if (currentCommentScopeIsAllFiles()) {
            return comments;
        }
        return comments.filter(comment => comment.filePath === currentFile);
    }

    function renderCommentList() {
        const list = document.getElementById('comment-list');
        list.innerHTML = '';

        const items = visibleComments();
        if (!items.length) {
            const empty = document.createElement('div');
            empty.className = 'rounded border border-dashed border-gray-300 px-3 py-2 text-xs text-gray-500';
            empty.textContent = currentCommentScopeIsAllFiles()
                ? 'No comments in this project yet.'
                : `No comments for ${currentFile}.`;
            list.appendChild(empty);
            return;
        }

        items.forEach(comment => {
            const card = document.createElement('div');
            card.className = 'rounded border border-amber-200 bg-amber-50 p-2 space-y-2 overflow-hidden';

            const topRow = document.createElement('div');
            topRow.className = 'flex items-start justify-between gap-2';

            const meta = document.createElement('div');
            meta.className = 'text-[11px] text-amber-900 truncate flex-1 min-w-0';
            const rangeLabel = comment.startLine === comment.endLine
                ? `${comment.filePath}:${comment.startLine}`
                : `${comment.filePath}:${comment.startLine}-${comment.endLine}`;
            meta.textContent = `${comment.authorEmail} • ${rangeLabel}`;
            topRow.appendChild(meta);

            const actions = document.createElement('div');
            actions.className = 'flex items-center gap-1';

            const jumpBtn = document.createElement('button');
            jumpBtn.type = 'button';
            jumpBtn.className = 'rounded border border-amber-300 bg-white px-1.5 py-0.5 text-[11px] text-amber-800 hover:bg-amber-100';
            jumpBtn.textContent = 'Jump';
            jumpBtn.onclick = () => jumpToComment(comment);
            actions.appendChild(jumpBtn);

            if (comment.canDelete) {
                const deleteBtn = document.createElement('button');
                deleteBtn.type = 'button';
                deleteBtn.className = 'rounded border border-red-300 bg-white px-1.5 py-0.5 text-[11px] text-red-700 hover:bg-red-50';
                deleteBtn.textContent = 'Delete';
                deleteBtn.onclick = () => deleteComment(comment.id);
                actions.appendChild(deleteBtn);
            }

            topRow.appendChild(actions);
            card.appendChild(topRow);

            const body = document.createElement('p');
            body.className = 'text-sm text-gray-800 whitespace-pre-wrap break-words';
            body.textContent = comment.body;
            card.appendChild(body);

            if (comment.snippet) {
                const snippet = document.createElement('p');
                snippet.className = 'rounded border border-amber-200 bg-white px-2 py-1 text-xs italic text-gray-600';
                snippet.textContent = `"${comment.snippet}"`;
                card.appendChild(snippet);
            }

            const created = document.createElement('div');
            created.className = 'text-[11px] text-gray-500';
            created.textContent = formatCommentCreatedAt(comment.created);
            card.appendChild(created);

            list.appendChild(card);
        });
    }

    function escapeForHover(value) {
        return String(value || '')
            .replace(/\\/g, '\\\\')
            .replace(/\|/g, '\\|');
    }

    function renderCommentDecorations() {
        if (!editor || !currentFileIsText) {
            if (editor) {
                commentDecorations = editor.deltaDecorations(commentDecorations, []);
            }
            return;
        }

        const model = editor.getModel();
        if (!model) {
            return;
        }

        const lineCount = Math.max(1, model.getLineCount());
        const currentFileComments = comments.filter(comment => comment.filePath === currentFile);
        const nextDecorations = currentFileComments.map(comment => {
            const startLine = clampLineValue(comment.startLine, 1, lineCount);
            const endLine = clampLineValue(comment.endLine, startLine, lineCount);
            const hoverParts = [
                `${comment.authorEmail}`,
                `${comment.startLine === comment.endLine ? comment.startLine : `${comment.startLine}-${comment.endLine}`}`,
            ];
            const hoverBody = escapeForHover(comment.body);
            return {
                range: new monaco.Range(startLine, 1, endLine, 1),
                options: {
                    isWholeLine: true,
                    className: 'comment-line-highlight',
                    linesDecorationsClassName: 'comment-line-decoration',
                    hoverMessage: [{ value: `${hoverParts.join(' • ')}\n\n${hoverBody}` }],
                },
            };
        });

        commentDecorations = editor.deltaDecorations(commentDecorations, nextDecorations);
    }

    async function loadComments() {
        try {
            const response = await fetch(`/api/projects/${projectId}/comments`);
            if (!response.ok) {
                setStatus('Failed to load comments', true);
                return;
            }
            comments = await response.json();
            renderCommentList();
            renderCommentDecorations();
        } catch (_) {
            setStatus('Failed to load comments', true);
        }
    }

    async function addComment() {
        if (!canComment) {
            setStatus('Your role cannot create comments', true);
            return;
        }

        if (!currentFileIsText) {
            setStatus('Open a text file to add comments', true);
            return;
        }

        updateCommentTargetFromEditor(true);
        const bodyInput = document.getElementById('comment-input');
        const body = bodyInput.value.trim();
        if (!body) {
            setStatus('Comment cannot be empty', true);
            return;
        }
        if (body.length > MAX_COMMENT_BODY_LENGTH) {
            setStatus(`Comment too long (max ${MAX_COMMENT_BODY_LENGTH} chars)`, true);
            return;
        }

        const payload = new URLSearchParams({
            filePath: commentTarget.filePath,
            startLine: String(commentTarget.startLine),
            endLine: String(commentTarget.endLine),
            body,
            snippet: commentTarget.snippet || '',
        });

        try {
            const response = await fetch(`/api/projects/${projectId}/comments`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
                body: payload.toString(),
            });
            if (!response.ok) {
                const message = await response.text();
                setStatus(message || 'Failed to add comment', true);
                return;
            }

            bodyInput.value = '';
            await loadComments();
            setStatus(`Comment added at ${commentTarget.filePath}:${commentTarget.startLine}`);
        } catch (_) {
            setStatus('Failed to add comment', true);
        }
    }

    async function deleteComment(commentID) {
        if (!confirm('Delete this comment?')) {
            return;
        }

        try {
            const response = await fetch(`/api/projects/${projectId}/comments/${encodeURIComponent(commentID)}`, {
                method: 'DELETE',
            });
            if (!response.ok) {
                setStatus('Failed to delete comment', true);
                return;
            }
            await loadComments();
            setStatus('Comment deleted');
        } catch (_) {
            setStatus('Failed to delete comment', true);
        }
    }

    async function jumpToComment(comment) {
        await openFile(comment.filePath);
        if (!currentFileIsText || !editor) {
            return;
        }

        const model = editor.getModel();
        if (!model) {
            return;
        }

        const maxLine = model.getLineCount();
        const startLine = clampLineValue(comment.startLine, 1, maxLine);
        const endLine = clampLineValue(comment.endLine, startLine, maxLine);
        editor.revealLineInCenter(startLine);
        editor.setSelection(new monaco.Range(startLine, 1, endLine, model.getLineMaxColumn(endLine)));
        editor.focus();
        highlightEditorRange(startLine, endLine);
        setStatus(`Jumped to ${comment.filePath}:${comment.startLine}`);
    }

    function clearFilePreview() {
        if (currentPreviewURL) {
            URL.revokeObjectURL(currentPreviewURL);
            currentPreviewURL = null;
        }
        document.getElementById('file-preview').innerHTML = '';
    }

    function normalizeContentType(contentType) {
        if (!contentType) return '';
        return contentType.split(';')[0].trim().toLowerCase();
    }

    function guessContentTypeFromFilename(filename) {
        const ext = filename.includes('.') ? filename.split('.').pop().toLowerCase() : '';
        const map = {
            png: 'image/png',
            jpg: 'image/jpeg',
            jpeg: 'image/jpeg',
            gif: 'image/gif',
            webp: 'image/webp',
            svg: 'image/svg+xml',
            ico: 'image/x-icon',
            bmp: 'image/bmp',
            pdf: 'application/pdf',
            mp4: 'video/mp4',
            webm: 'video/webm',
            mov: 'video/quicktime',
            mp3: 'audio/mpeg',
            wav: 'audio/wav',
            ogg: 'audio/ogg',
            txt: 'text/plain',
            md: 'text/markdown',
            json: 'application/json',
            csv: 'text/csv',
            xml: 'application/xml',
            html: 'text/html',
        };

        return map[ext] || 'application/octet-stream';
    }

    function resolveContentType(filename, contentType) {
        const normalized = normalizeContentType(contentType);
        if (normalized && normalized !== 'application/octet-stream') {
            return normalized;
        }
        return guessContentTypeFromFilename(filename);
    }

    function isTextualContentType(contentType) {
        if (!contentType) return false;
        if (contentType.startsWith('text/')) return true;
        return [
            'application/json',
            'application/xml',
            'application/javascript',
            'application/x-yaml',
        ].includes(contentType);
    }

    function formatBytes(size) {
        if (size < 1024) return `${size} B`;
        if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
        return `${(size / (1024 * 1024)).toFixed(1)} MB`;
    }

    async function renderBinaryPreview(blob, filename, contentType) {
        clearFilePreview();

        const preview = document.getElementById('file-preview');
        const wrapper = document.createElement('div');
        wrapper.className = 'h-full flex flex-col p-4 gap-3 overflow-auto';

        const header = document.createElement('div');
        header.className = 'text-sm text-gray-600';
        header.textContent = `${filename} (${contentType || 'unknown'}, ${formatBytes(blob.size)})`;
        wrapper.appendChild(header);

        const content = document.createElement('div');
        content.className = 'flex-1 min-h-0';
        wrapper.appendChild(content);

        const objectURL = URL.createObjectURL(blob);
        currentPreviewURL = objectURL;

        if (contentType.startsWith('image/')) {
            const img = document.createElement('img');
            img.src = objectURL;
            img.alt = filename;
            img.className = 'max-w-full max-h-full mx-auto rounded border bg-gray-50';
            content.className = 'flex-1 min-h-0 flex items-center justify-center';
            content.appendChild(img);
        } else if (contentType === 'application/pdf') {
            const iframe = document.createElement('iframe');
            iframe.src = objectURL;
            iframe.className = 'w-full h-full border rounded bg-white';
            content.appendChild(iframe);
        } else if (contentType.startsWith('audio/')) {
            const audio = document.createElement('audio');
            audio.src = objectURL;
            audio.controls = true;
            audio.className = 'w-full';
            content.className = 'flex-1 min-h-0 flex items-start';
            content.appendChild(audio);
        } else if (contentType.startsWith('video/')) {
            const video = document.createElement('video');
            video.src = objectURL;
            video.controls = true;
            video.className = 'max-w-full max-h-full rounded bg-black';
            content.className = 'flex-1 min-h-0 flex items-center justify-center';
            content.appendChild(video);
        } else if (isTextualContentType(contentType)) {
            const text = await blob.text();
            const pre = document.createElement('pre');
            pre.className = 'h-full overflow-auto bg-gray-900 text-gray-100 p-4 rounded text-sm whitespace-pre-wrap break-words';
            pre.textContent = text;
            content.appendChild(pre);
        } else {
            const unsupported = document.createElement('div');
            unsupported.className = 'h-full flex flex-col items-center justify-center gap-2 text-gray-600';
            unsupported.innerHTML = '<p>No inline preview for this file type.</p>';

            const link = document.createElement('a');
            link.href = objectURL;
            link.target = '_blank';
            link.rel = 'noopener noreferrer';
            link.download = filename;
            link.className = 'text-blue-600 hover:text-blue-800 underline';
            link.textContent = 'Open or download file';
            unsupported.appendChild(link);
            content.appendChild(unsupported);
        }

        preview.appendChild(wrapper);
    }

    async function save() {
        if (!canWrite) {
            setStatus('Read-only access', true);
            return;
        }

        if (!currentFileIsText) {
            setStatus('Preview-only file: save is disabled');
            return;
        }

        setStatus('Saving...');
        const content = editor.getValue();
        fileContents[currentFile] = content;

        try {
            const response = await fetch(buildFileURL(currentFile), {
                method: 'PUT',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
                body: saveBody(currentFile, content)
            });

            if (response.ok) {
                const etag = response.headers.get('ETag');
                if (etag) { fileHashes[currentFile] = etag; }
                delete unsavedChanges[currentFile];
                updateFileList();
                setStatus('Saved');
                loadGitStatus(false);
            } else if (response.status === 409) {
                setStatus('This file changed on disk since you opened it. Reload it before saving.', true);
            } else {
                setStatus('Save failed', true);
            }
        } catch (e) {
            setStatus('Save failed', true);
        }
    }

    async function saveAll() {
        if (!canWrite) {
            setStatus('Read-only access', true);
            return false;
        }

        for (const filename of Object.keys(unsavedChanges)) {
            const content = fileContents[filename];
            const response = await fetch(buildFileURL(filename), {
                method: 'PUT',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
                body: saveBody(filename, content)
            });
            if (response.status === 409) {
                setStatus(`${filename} changed on disk since you opened it. Reload it before saving.`, true);
                return false;
            }
            if (!response.ok) {
                setStatus(`Failed to save ${filename}`, true);
                return false;
            }
            const etag = response.headers.get('ETag');
            if (etag) { fileHashes[filename] = etag; }
        }
        unsavedChanges = {};
        updateFileList();
        return true;
    }

    async function compile() {
        if (!canWrite) {
            setStatus('Read-only access', true);
            return;
        }

        const selectedEntry = compileTarget;
        if (!selectedEntry || !filesByPath[selectedEntry] || filesByPath[selectedEntry].isDir || !isCompilableFile(selectedEntry)) {
            setStatus('Select a .tex, .typ, or .md file to compile', true);
            return;
        }

        setStatus('Saving all files...');
        const saved = await saveAll();
        if (!saved) return;

        setStatus('Compiling...');
        hideError();
        updateCompileStat(null);

        const compileBtn = document.getElementById('compile-btn');
        compileBtn.disabled = true;
        compileBtn.textContent = 'Compiling...';

        try {
            const compileStartedAt = performance.now();
            const response = await fetch(`/compile/${projectId}`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
                body: `entry=${encodeURIComponent(selectedEntry)}`
            });

            const serverCompileMs = Number(
                response.headers.get('X-Compile-Ms') || response.headers.get('X-Latex-Compile-Ms')
            );
            const compileMs = Number.isFinite(serverCompileMs) && serverCompileMs >= 0
                ? serverCompileMs
                : performance.now() - compileStartedAt;
            const responseEntry = response.headers.get('X-Compile-Entry') || selectedEntry;

            if (response.ok) {
                updateCompileStat(compileMs);
                const pdfData = await response.arrayBuffer();
                compileTarget = responseEntry;
                updateEntryDisplay();
                renderFileList(fileList);
                const loaded = await ensureTextFileCached(compileTarget);
                if (!loaded) {
                    fileContents[compileTarget] = '';
                }
                await renderPDF(pdfData);
                setStatus(`Compiled ${responseEntry}`);
            } else {
                updateCompileStat(compileMs);
                const errorText = await response.text();
                showError(errorText);
                setStatus('Compilation failed', true);
            }
        } catch (e) {
            showError('Network error: ' + e.message);
            setStatus('Compilation failed', true);
        } finally {
            compileBtn.disabled = false;
            compileBtn.textContent = 'Compile';
        }
    }

    async function deleteFile(filename) {
        if (!canWrite) {
            setStatus('Read-only access', true);
            return;
        }

        if (!confirm(`Delete ${filename}?`)) return;

        const response = await fetch(buildFileURL(filename), {
            method: 'DELETE'
        });

        if (response.ok) {
            const pathPrefix = `${filename}/`;
            Object.keys(fileContents).forEach(path => {
                if (path === filename || path.startsWith(pathPrefix)) {
                    delete fileContents[path];
                }
            });
            Object.keys(unsavedChanges).forEach(path => {
                if (path === filename || path.startsWith(pathPrefix)) {
                    delete unsavedChanges[path];
                }
            });

            const nextExpanded = new Set();
            expandedFolders.forEach(path => {
                if (path !== filename && !path.startsWith(pathPrefix)) {
                    nextExpanded.add(path);
                }
            });
            expandedFolders = nextExpanded;
            expandedFolders.add('');

            const deletedCurrent = currentFile === filename || currentFile.startsWith(pathPrefix);
            await loadFileList();
            await loadComments();

            if (deletedCurrent) {
                const fallbackFile = getDefaultOpenFile();
                if (fallbackFile) {
                    await openFile(fallbackFile);
                }
            }
            setStatus(`${filename} deleted`);
        } else {
            setStatus('Failed to delete file', true);
        }
    }

    let renameTargetPath = '';

    function openRenameModal(path) {
        renameTargetPath = path;
        const input = document.getElementById('rename-input');
        input.value = getBaseName(path);
        document.getElementById('rename-modal').classList.remove('hidden');
        input.focus();
        input.select();
    }

    function closeRenameModal() {
        document.getElementById('rename-modal').classList.add('hidden');
        document.getElementById('rename-input').value = '';
        renameTargetPath = '';
    }

    async function confirmRename() {
        if (!canWrite) {
            setStatus('Read-only access', true);
            return;
        }

        const newName = document.getElementById('rename-input').value.trim();
        if (!newName || !renameTargetPath) {
            closeRenameModal();
            return;
        }

        const oldName = getBaseName(renameTargetPath);
        if (newName === oldName) {
            closeRenameModal();
            return;
        }

        const parentDir = getParentPath(renameTargetPath);
        const newPath = parentDir ? `${parentDir}/${newName}` : newName;

        setStatus(`Renaming ${oldName}...`);

        try {
            const response = await fetch(`/api/projects/${projectId}/files/move`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
                body: `source=${encodeURIComponent(renameTargetPath)}&target=${encodeURIComponent(newPath)}`,
            });

            if (!response.ok) {
                const errorText = await response.text();
                setStatus(errorText || 'Failed to rename', true);
                closeRenameModal();
                return;
            }

            remapPathState(renameTargetPath, newPath);
            await loadFileList();
            await loadComments();

            if (currentFile === renameTargetPath || currentFile.startsWith(renameTargetPath + '/')) {
                const updatedPath = currentFile.replace(renameTargetPath, newPath);
                if (filesByPath[updatedPath] && !filesByPath[updatedPath].isDir) {
                    await openFile(updatedPath);
                }
            }

            setStatus(`Renamed to ${newName}`);
        } catch (_) {
            setStatus('Failed to rename', true);
        }
        closeRenameModal();
    }

    document.getElementById('cancel-rename').onclick = closeRenameModal;
    document.getElementById('confirm-rename').onclick = confirmRename;
    document.getElementById('rename-input').onkeydown = (e) => {
        if (e.key === 'Enter') confirmRename();
        if (e.key === 'Escape') closeRenameModal();
    };

    async function createEntry(rawPath, type = 'file') {
        if (!canWrite) {
            setStatus('Read-only access', true);
            return '';
        }

        const normalizedPath = normalizeClientPath(rawPath.trim());
        if (!normalizedPath) {
            return '';
        }

        const response = await fetch(`/api/projects/${projectId}/files`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
            body: `filename=${encodeURIComponent(normalizedPath)}&type=${encodeURIComponent(type)}`
        });

        if (!response.ok) {
            return '';
        }

        if (type !== 'dir') {
            fileContents[normalizedPath] = '';
        }

        ensureFolderExpanded(getParentPath(normalizedPath));
        await loadFileList();
        return normalizedPath;
    }

    document.getElementById('new-file-btn').onclick = () => {
        if (!canWrite) {
            setStatus('Read-only access', true);
            return;
        }
        document.getElementById('new-file-modal').classList.remove('hidden');
        document.getElementById('new-file-name').focus();
    };

    document.getElementById('cancel-new-file').onclick = () => {
        document.getElementById('new-file-modal').classList.add('hidden');
        document.getElementById('new-file-name').value = '';
    };

    document.getElementById('confirm-new-file').onclick = async () => {
        if (!canWrite) {
            setStatus('Read-only access', true);
            return;
        }

        const filename = document.getElementById('new-file-name').value.trim();
        if (!filename) return;

        const createdPath = await createEntry(filename, 'file');
        if (!createdPath) {
            alert('Failed to create file');
            return;
        }

        document.getElementById('new-file-modal').classList.add('hidden');
        document.getElementById('new-file-name').value = '';
        await openFile(createdPath);
    };

    document.getElementById('new-file-name').onkeydown = (e) => {
        if (e.key === 'Enter') document.getElementById('confirm-new-file').click();
        if (e.key === 'Escape') document.getElementById('cancel-new-file').click();
    };

    document.getElementById('new-folder-btn').onclick = async () => {
        if (!canWrite) {
            setStatus('Read-only access', true);
            return;
        }

        const folderName = prompt('Folder name (e.g. chapters/section1)');
        if (!folderName) return;

        const createdPath = await createEntry(folderName, 'dir');
        if (!createdPath) {
            alert('Failed to create folder');
            return;
        }
        expandedFolders.add(createdPath);
        updateFileList();
        setStatus(`Folder created: ${createdPath}`);
    };

    document.getElementById('upload-btn').onclick = () => {
        if (!canWrite) {
            setStatus('Read-only access', true);
            return;
        }
        document.getElementById('file-input').click();
    };

    document.getElementById('file-input').onchange = async (e) => {
        if (!canWrite) {
            e.target.value = '';
            setStatus('Read-only access', true);
            return;
        }

        const files = e.target.files;
        for (const file of files) {
            const formData = new FormData();
            formData.append('file', file);

            await fetch(`/api/projects/${projectId}/upload`, {
                method: 'POST',
                body: formData
            });
        }
        e.target.value = '';
        await loadFileList();
        setStatus('Files uploaded');
    };

    function setStatus(text, isError = false) {
        const status = document.getElementById('status');
        status.textContent = text;
        status.className = isError ? 'text-sm text-red-500' : 'text-sm text-gray-500';
    }

    function formatGitTimestamp(value) {
        if (!value) {
            return 'Never';
        }

        const normalized = value.includes('T') ? value : `${value.replace(' ', 'T')}Z`;
        const parsed = new Date(normalized);
        if (Number.isNaN(parsed.getTime())) {
            return value;
        }
        return parsed.toLocaleString();
    }

    function setElementDisabled(element, disabled) {
        if (!element) {
            return;
        }
        element.disabled = disabled;
        element.classList.toggle('opacity-50', disabled);
        element.classList.toggle('cursor-not-allowed', disabled);
    }

    function setGitBadge(text, tone = 'neutral') {
        const badge = document.getElementById('git-status-badge');
        badge.textContent = text;

        const toneClasses = {
            neutral: 'rounded bg-gray-700 px-2 py-1 text-[10px] font-medium text-gray-200',
            success: 'rounded bg-emerald-600/20 px-2 py-1 text-[10px] font-medium text-emerald-200',
            warning: 'rounded bg-amber-500/20 px-2 py-1 text-[10px] font-medium text-amber-200',
            error: 'rounded bg-red-500/20 px-2 py-1 text-[10px] font-medium text-red-200',
        };
        badge.className = toneClasses[tone] || toneClasses.neutral;
    }

    let gitFeedbackTimer = null;

    function escapeHTML(str) {
        const div = document.createElement('div');
        div.appendChild(document.createTextNode(str));
        return div.innerHTML;
    }

    function showGitFeedback(message, isError = false) {
        const el = document.getElementById('git-feedback');
        if (!el) return;

        el.className = isError
            ? 'mx-3 mt-3 rounded px-3 py-2 text-xs font-medium bg-red-500/20 text-red-200 border border-red-500/30'
            : 'mx-3 mt-3 rounded px-3 py-2 text-xs font-medium bg-emerald-500/20 text-emerald-200 border border-emerald-500/30';

        el.textContent = '';
        const body = document.createElement('div');
        body.textContent = message;
        body.className = 'whitespace-pre-wrap break-words max-h-48 overflow-auto font-mono leading-relaxed';
        el.appendChild(body);

        if (gitFeedbackTimer) {
            clearTimeout(gitFeedbackTimer);
            gitFeedbackTimer = null;
        }

        if (isError) {
            // Git errors are multi-line and actionable, so they stay until dismissed.
            const dismiss = document.createElement('button');
            dismiss.type = 'button';
            dismiss.textContent = 'Dismiss';
            dismiss.className = 'mt-2 rounded border border-red-500/40 px-2 py-0.5 text-[11px] font-medium hover:bg-red-500/20';
            dismiss.addEventListener('click', () => el.classList.add('hidden'));
            el.appendChild(dismiss);
        } else {
            gitFeedbackTimer = setTimeout(() => {
                el.classList.add('hidden');
                gitFeedbackTimer = null;
            }, 4000);
        }

        el.classList.remove('hidden');
    }

    function renderChangedFiles(fileNames) {
        const list = document.getElementById('git-changed-files-list');
        const countEl = document.getElementById('git-changed-files-count');
        if (!list || !countEl) return;

        const names = fileNames || [];
        countEl.textContent = `(${names.length})`;

        if (names.length === 0) {
            list.innerHTML = '<p class="text-[11px] text-gray-500">No changed files.</p>';
            return;
        }

        const statusColors = {
            'M': 'text-amber-400',
            'A': 'text-emerald-400',
            'D': 'text-red-400',
            '?': 'text-blue-400',
            'R': 'text-purple-400',
        };

        let html = '';
        for (const entry of names) {
            // Porcelain format is exactly two status columns, a space, then the
            // path. The columns may be blank, so never trim before slicing or
            // the filename shifts.
            const parsed = /^(..) (.*)$/.exec(entry);
            const code = (parsed ? parsed[1] : entry.slice(0, 2)).trim() || '?';
            const name = parsed ? parsed[2] : entry.slice(2).trim();
            const colorClass = statusColors[code.charAt(0)] || statusColors[code.charAt(1)] || 'text-gray-400';
            html += `<div class="flex items-center gap-2 text-xs">
                <span class="${colorClass} font-mono font-bold w-4 shrink-0">${escapeHTML(code)}</span>
                <span class="text-gray-300 truncate">${escapeHTML(name)}</span>
            </div>`;
        }
        list.innerHTML = html;
    }

    function isSSHRemoteURL(value) {
        const normalized = (value || '').trim().toLowerCase();
        if (normalized.startsWith('ssh://')) {
            return true;
        }
        return normalized.includes('@') && normalized.includes(':') && !normalized.includes('://');
    }

    function updateGitUI(nextStatus) {
        gitStatus = nextStatus || { configured: false, repoPresent: false };

        const configured = !!gitStatus.configured;
        const repoPresent = !!gitStatus.repoPresent;
        const hasChanges = !!gitStatus.hasUncommittedChanges;
        const changedFiles = Number(gitStatus.changedFiles || 0);
        const hasSSHKey = !!gitStatus.hasSSHKey;
        const needsSSHKey = configured && isSSHRemoteURL(gitStatus.remoteURL || '');
        const missingSSHKey = needsSSHKey && !hasSSHKey;

        let summary = 'Not configured for Git sync.';
        let workingTree = 'Unavailable';
        let lastCommit = 'Unavailable';

        if (!configured) {
            setGitBadge('Not linked', 'neutral');
        } else if (missingSSHKey) {
            summary = 'This project uses an SSH remote. Generate your account SSH key on the home page before syncing.';
            workingTree = repoPresent ? (hasChanges
                ? `${changedFiles} uncommitted change${changedFiles === 1 ? '' : 's'}`
                : 'Clean') : 'Unavailable';
            lastCommit = gitStatus.lastCommit || 'Unavailable';
            setGitBadge('SSH key required', 'error');
        } else if (!repoPresent) {
            summary = 'Git settings exist, but the local repository is missing.';
            workingTree = 'Repository missing';
            lastCommit = gitStatus.lastCommit || 'Unavailable';
            setGitBadge('Repo missing', 'error');
        } else {
            summary = `Tracking ${gitStatus.branch || 'current branch'} on origin.`;
            if (gitStatus.currentBranch && gitStatus.branch && gitStatus.currentBranch !== gitStatus.branch) {
                summary += ` Current checkout: ${gitStatus.currentBranch}.`;
            }
            workingTree = hasChanges
                ? `${changedFiles} uncommitted change${changedFiles === 1 ? '' : 's'}`
                : 'Clean';
            lastCommit = gitStatus.lastCommit || 'No commits yet';
            setGitBadge(hasChanges ? 'Local changes' : 'Connected', hasChanges ? 'warning' : 'success');
        }

        document.getElementById('git-config-summary').textContent = summary;
        document.getElementById('git-remote-display').textContent = configured ? (gitStatus.remoteURL || '-') : 'Not configured';
        document.getElementById('git-branch-display').textContent = configured ? (gitStatus.branch || '-') : '-';
        document.getElementById('git-last-sync-display').textContent = configured ? formatGitTimestamp(gitStatus.lastSync) : 'Never';
        document.getElementById('git-working-tree-display').textContent = workingTree;
        document.getElementById('git-sync-state-display').textContent = describeSyncState(gitStatus, configured, repoPresent);
        document.getElementById('git-last-commit-display').textContent = lastCommit;
        updateDivergedPanel(gitStatus, configured, repoPresent);
        document.getElementById('git-user-key-display').textContent = hasSSHKey
            ? (gitStatus.sshKeyFingerprint || 'Available')
            : 'Not generated';

        const remoteInput = document.getElementById('git-remote-url');
        if (remoteInput && document.activeElement !== remoteInput) {
            remoteInput.value = configured ? (gitStatus.remoteURL || '') : '';
        }

        const branchInput = document.getElementById('git-branch');
        if (branchInput && document.activeElement !== branchInput) {
            branchInput.value = configured ? (gitStatus.branch || '') : '';
        }

        const pullDisabled = !canWrite || !configured || !repoPresent || hasChanges || missingSSHKey;
        setElementDisabled(document.getElementById('git-pull-btn'), pullDisabled);
        setElementDisabled(document.getElementById('git-push-btn'), !canWrite || !configured || missingSSHKey);
        setElementDisabled(document.getElementById('git-reset-btn'), !canWrite || !configured || !repoPresent || !hasChanges);

        const pullHint = document.getElementById('git-pull-disabled-hint');
        if (pullHint) {
            pullHint.classList.toggle('hidden', !pullDisabled || !configured || !repoPresent || missingSSHKey);
        }

        renderChangedFiles(gitStatus.changedFileNames);
    }

    async function loadGitStatus(showFailure = true) {
        try {
            const response = await fetch(`/api/projects/${projectId}/git/status`);
            if (!response.ok) {
                const message = (await response.text()).trim() || 'Failed to load Git status';
                if (showFailure) {
                    setStatus(message, true);
                }
                return;
            }

            updateGitUI(await response.json());
        } catch (_) {
            if (showFailure) {
                setStatus('Failed to load Git status', true);
            }
        }
    }

    async function reloadWorkspaceFromDisk(preferredFile = '') {
        const targetFile = preferredFile || currentFile;
        clearFilePreview();
        fileContents = {};
        unsavedChanges = {};
        currentFile = '';
        currentFileIsText = true;

        await loadFileList();
        if (targetFile && filesByPath[targetFile] && !filesByPath[targetFile].isDir) {
            await openFile(targetFile);
        }
    }

    async function saveGitConfig(event) {
        event.preventDefault();

        const form = document.getElementById('git-config-form');
        if (!form) {
            return;
        }

        const saveButton = document.getElementById('git-config-save-btn');
        const formData = new FormData(form);
        setElementDisabled(saveButton, true);
        setStatus('Saving Git settings...');

        try {
            const response = await fetch(`/api/projects/${projectId}/git/config`, {
                method: 'POST',
                body: formData,
            });
            if (!response.ok) {
                const message = (await response.text()).trim() || 'Failed to save Git settings';
                setStatus(message, true);
                return;
            }

            const payload = await response.json();
            await loadGitStatus(false);
            setStatus(payload.status || 'Git sync settings saved');
        } catch (_) {
            setStatus('Failed to save Git settings', true);
        } finally {
            setElementDisabled(saveButton, false);
        }
    }

    function commitCountLabel(count) {
        return `${count} commit${count === 1 ? '' : 's'}`;
    }

    function describeSyncState(status, configured, repoPresent) {
        if (!configured || !repoPresent) return 'Unavailable';
        if (!status.trackingRemote) return 'No remote branch fetched yet';

        const ahead = Number(status.ahead || 0);
        const behind = Number(status.behind || 0);
        if (ahead === 0 && behind === 0) return 'Up to date with origin';
        if (ahead > 0 && behind > 0) {
            return `Diverged: ${commitCountLabel(ahead)} ahead, ${commitCountLabel(behind)} behind`;
        }
        if (ahead > 0) return `${commitCountLabel(ahead)} ahead of origin`;
        return `${commitCountLabel(behind)} behind origin`;
    }

    function updateDivergedPanel(status, configured, repoPresent) {
        const panel = document.getElementById('git-diverged-panel');
        if (!panel) return;

        const diverged = configured && repoPresent && !!status.diverged;
        if (!diverged) {
            panel.classList.add('hidden');
            return;
        }

        const summary = document.getElementById('git-diverged-summary');
        if (summary) {
            summary.textContent = `${commitCountLabel(Number(status.ahead || 0))} of yours, `
                + `${commitCountLabel(Number(status.behind || 0))} on the remote`;
        }
        panel.classList.remove('hidden');
    }

    // Shared by the two divergence recovery actions, which differ only in
    // endpoint, spinner, and the confirmation they require.
    async function runGitRecovery({ endpoint, buttonId, spinnerId, fallbackError }) {
        const button = document.getElementById(buttonId);
        const spinner = document.getElementById(spinnerId);
        setElementDisabled(button, true);
        if (spinner) spinner.classList.remove('hidden');

        try {
            const response = await fetch(`/api/projects/${projectId}/git/${endpoint}`, {
                method: 'POST',
            });
            if (!response.ok) {
                showGitFeedback((await response.text()).trim() || fallbackError, true);
                return;
            }

            const payload = await response.json();
            await reloadWorkspaceFromDisk(currentFile);
            await loadComments();
            await loadGitStatus(false);
            showGitFeedback(payload.status || 'Done');
        } catch (_) {
            showGitFeedback(fallbackError, true);
        } finally {
            if (spinner) spinner.classList.add('hidden');
            setElementDisabled(button, false);
            updateGitUI(gitStatus);
        }
    }

    async function gitPullRebase() {
        if (Object.keys(unsavedChanges).length > 0) {
            showGitFeedback('Save or discard editor changes before rebasing.', true);
            return;
        }
        await runGitRecovery({
            endpoint: 'pull-rebase',
            buttonId: 'git-rebase-btn',
            spinnerId: 'git-rebase-spinner',
            fallbackError: 'Failed to rebase onto the remote branch.',
        });
    }

    async function gitDiscardLocalCommits() {
        const ahead = Number(gitStatus.ahead || 0);
        const confirmed = confirm(
            `This permanently discards ${commitCountLabel(ahead)} that exist only in this project, `
            + `along with any uncommitted changes, so it matches origin/${gitStatus.branch || 'main'}.\n\n`
            + 'This cannot be undone. Continue?'
        );
        if (!confirmed) return;

        await runGitRecovery({
            endpoint: 'reset-to-remote',
            buttonId: 'git-discard-btn',
            spinnerId: 'git-discard-spinner',
            fallbackError: 'Failed to reset to the remote branch.',
        });
    }

    async function gitPull() {
        if (!gitStatus.configured) {
            showGitFeedback('Configure Git sync before pulling.', true);
            return;
        }
        if (Object.keys(unsavedChanges).length > 0) {
            showGitFeedback('Save and push or discard editor changes before pulling.', true);
            return;
        }

        const pullButton = document.getElementById('git-pull-btn');
        const pullSpinner = document.getElementById('git-pull-spinner');
        setElementDisabled(pullButton, true);
        if (pullSpinner) pullSpinner.classList.remove('hidden');

        try {
            const response = await fetch(`/api/projects/${projectId}/git/pull`, {
                method: 'POST',
            });
            if (!response.ok) {
                const message = (await response.text()).trim() || 'Failed to pull latest changes';
                // The attempt already fetched, so refreshing now reveals the
                // divergence panel with the recovery actions.
                await loadGitStatus(false);
                showGitFeedback(message, true);
                return;
            }

            const payload = await response.json();
            const preferredFile = currentFile;
            await reloadWorkspaceFromDisk(preferredFile);
            await loadComments();
            await loadGitStatus(false);
            showGitFeedback(payload.status || 'Pulled latest changes');
        } catch (_) {
            showGitFeedback('Failed to pull latest changes.', true);
        } finally {
            if (pullSpinner) pullSpinner.classList.add('hidden');
            updateGitUI(gitStatus);
        }
    }

    async function gitPush() {
        if (!gitStatus.configured) {
            showGitFeedback('Configure Git sync before pushing.', true);
            return;
        }

        const pushButton = document.getElementById('git-push-btn');
        const pushSpinner = document.getElementById('git-push-spinner');
        setElementDisabled(pushButton, true);
        if (pushSpinner) pushSpinner.classList.remove('hidden');

        try {
            if (!await saveAll()) {
                return;
            }

            const formData = new FormData();
            const commitMessage = document.getElementById('git-commit-message');
            if (commitMessage && commitMessage.value.trim()) {
                formData.append('commitMessage', commitMessage.value.trim());
            }

            const response = await fetch(`/api/projects/${projectId}/git/push`, {
                method: 'POST',
                body: formData,
            });
            if (!response.ok) {
                const message = (await response.text()).trim() || 'Failed to push changes';
                showGitFeedback(message, true);
                return;
            }

            const payload = await response.json();
            if (commitMessage) {
                commitMessage.value = '';
            }
            await loadGitStatus(false);
            showGitFeedback(payload.status || 'Pushed changes to remote');
        } catch (_) {
            showGitFeedback('Failed to push changes.', true);
        } finally {
            if (pushSpinner) pushSpinner.classList.add('hidden');
            updateGitUI(gitStatus);
        }
    }

    async function gitReset() {
        if (!gitStatus.configured || !gitStatus.repoPresent) {
            showGitFeedback('Nothing to reset.', true);
            return;
        }
        if (!confirm('Discard all uncommitted changes? This cannot be undone.')) {
            return;
        }

        const resetButton = document.getElementById('git-reset-btn');
        const resetSpinner = document.getElementById('git-reset-spinner');
        setElementDisabled(resetButton, true);
        if (resetSpinner) resetSpinner.classList.remove('hidden');

        try {
            const response = await fetch(`/api/projects/${projectId}/git/reset`, {
                method: 'POST',
            });
            if (!response.ok) {
                const message = (await response.text()).trim() || 'Failed to reset changes';
                showGitFeedback(message, true);
                return;
            }

            const payload = await response.json();
            const preferredFile = currentFile;
            await reloadWorkspaceFromDisk(preferredFile);
            await loadGitStatus(false);
            showGitFeedback(payload.status || 'Discarded all local changes');
        } catch (_) {
            showGitFeedback('Failed to reset changes.', true);
        } finally {
            if (resetSpinner) resetSpinner.classList.add('hidden');
            updateGitUI(gitStatus);
        }
    }

    function parseDownloadFilename(contentDisposition) {
        if (!contentDisposition) {
            return '';
        }
        const utf8Match = contentDisposition.match(/filename\*=UTF-8''([^;]+)/i);
        if (utf8Match && utf8Match[1]) {
            try {
                return decodeURIComponent(utf8Match[1]);
            } catch (_) {
                return utf8Match[1];
            }
        }
        const plainMatch = contentDisposition.match(/filename="?([^\";]+)"?/i);
        if (plainMatch && plainMatch[1]) {
            return plainMatch[1];
        }
        return '';
    }

    async function downloadBlob(url, fallbackFilename, successStatus, failureStatus) {
        try {
            const response = await fetch(url);
            if (!response.ok) {
                const message = (await response.text()).trim();
                setStatus(message || failureStatus, true);
                return;
            }

            const blob = await response.blob();
            const fileName = parseDownloadFilename(response.headers.get('Content-Disposition')) || fallbackFilename;
            const objectURL = URL.createObjectURL(blob);
            const link = document.createElement('a');
            link.href = objectURL;
            link.download = fileName;
            document.body.appendChild(link);
            link.click();
            document.body.removeChild(link);
            setTimeout(() => URL.revokeObjectURL(objectURL), 1000);
            setStatus(successStatus);
        } catch (_) {
            setStatus(failureStatus, true);
        }
    }

    async function downloadSourceArchive() {
        await downloadBlob(
            `/api/projects/${projectId}/download/source`,
            `${projectId}-source.zip`,
            'Downloaded source archive',
            'Failed to download source archive',
        );
    }

    async function downloadCompiledPDF() {
        const entryFile = compileTarget ? getBaseName(compileTarget) : 'document';
        const dotIndex = entryFile.lastIndexOf('.');
        const pdfName = `${dotIndex > 0 ? entryFile.slice(0, dotIndex) : entryFile}.pdf`;
        await downloadBlob(
            `/api/projects/${projectId}/download/pdf`,
            pdfName,
            'Downloaded PDF',
            'Failed to download PDF',
        );
    }

    if (initialErrorMessage) {
        setStatus(initialErrorMessage, true);
    } else if (initialStatusMessage) {
        setStatus(initialStatusMessage);
    }

    function showError(message) {
        document.getElementById('error-panel').classList.remove('hidden');
        document.getElementById('error-content').textContent = message;
    }

    function hideError() {
        document.getElementById('error-panel').classList.add('hidden');
    }

    document.getElementById('close-error').addEventListener('click', hideError);
    window.addEventListener('beforeunload', clearFilePreview);

    function applyWritePermissions() {
        if (canWrite) {
            return;
        }

        ['new-file-btn', 'new-folder-btn', 'upload-btn', 'save-btn', 'compile-btn', 'confirm-new-file']
            .forEach((id) => {
                const element = document.getElementById(id);
                if (!element) {
                    return;
                }
                element.disabled = true;
                element.classList.add('opacity-50', 'cursor-not-allowed');
            });
    }

    function updateZoomLabel() {
        document.getElementById('pdf-zoom-label').textContent = `${Math.round(pdfZoom * 100)}%`;
    }

    function clampPDFZoom(value) {
        return Math.min(MAX_PDF_ZOOM, Math.max(MIN_PDF_ZOOM, value));
    }

    function formatDuration(ms) {
        if (!Number.isFinite(ms)) {
            return '--';
        }
        if (ms < 1000) {
            return `${Math.round(ms)} ms`;
        }
        return `${(ms / 1000).toFixed(2)} s`;
    }

    function updateCompileStat(ms = null) {
        const stat = document.getElementById('pdf-compile-stat');
        if (!Number.isFinite(ms)) {
            stat.classList.add('hidden');
            stat.textContent = 'Compile: --';
            return;
        }

        stat.textContent = `Compile: ${formatDuration(ms)}`;
        stat.classList.remove('hidden');
    }

    async function setPDFZoom(nextZoom) {
        if (!pdfDocument) {
            return;
        }

        const clampedZoom = clampPDFZoom(nextZoom);
        if (Math.abs(clampedZoom - pdfZoom) < 0.001) {
            return;
        }

        pdfZoom = clampedZoom;
        updateZoomLabel();
        await renderPDFPages();
    }

    function normalizeWhitespace(value) {
        return value.replace(/\s+/g, ' ').trim();
    }

    function normalizePDFText(value) {
        return normalizeWhitespace(value.toLowerCase().replace(/[^a-z0-9\s]/g, ' '));
    }

    function normalizeTeXLine(value) {
        const withoutComments = value.split('%')[0];
        const withoutCommands = withoutComments
            .replace(/\\[a-zA-Z@]+\*?\s*(\[[^\]]*\])?/g, ' ')
            .replace(/[{}$&#_^~\\]/g, ' ');
        return normalizePDFText(withoutCommands);
    }

    function normalizeTypstLine(value) {
        const withoutComments = value.split('//')[0];
        const withoutMarkup = withoutComments
            .replace(/#[a-zA-Z@]+\s*/g, ' ')
            .replace(/[{}$&#_^~\\*\[\]<>`]/g, ' ');
        return normalizePDFText(withoutMarkup);
    }

    function normalizeSourceLine(value) {
        const ext = getFileExtension(compileTarget || '');
        if (ext === '.tex') {
            return normalizeTeXLine(value);
        }
        if (ext === '.typ') {
            return normalizeTypstLine(value);
        }
        return normalizePDFText(value);
    }

    function lineSearchNeedle(value) {
        const normalized = normalizeSourceLine(value);
        const ext = getFileExtension(compileTarget || '');
        const isTypst = ext === '.typ';
        if (normalized.length < (isTypst ? 3 : 6)) {
            return '';
        }

        const words = normalized
            .split(' ')
            .filter(word => word.length > (isTypst ? 1 : 2));
        if (words.length < (isTypst ? 1 : 2)) {
            return '';
        }

        return words.slice(0, isTypst ? 6 : 8).join(' ');
    }

    function clampUnitInterval(value) {
        if (!Number.isFinite(value)) {
            return 0.5;
        }
        return Math.min(1, Math.max(0, value));
    }

    function pickLineFromCandidates(candidates, positionRatio = 0.5) {
        if (!candidates || !candidates.length) {
            return null;
        }
        const ratio = clampUnitInterval(positionRatio);
        const index = Math.round(ratio * (candidates.length - 1));
        return candidates[index];
    }

    function buildLinePageMaps() {
        lineToPageMap = new Map();
        pageToLinesMap = new Map();

        const source = fileContents[compileTarget] || '';
        const lines = source.split(/\r?\n/);
        const pageNumbers = Array.from(pageTextMap.keys()).sort((a, b) => a - b);
        if (!lines.length || !pageNumbers.length) {
            return;
        }

        let preferredPageIndex = 0;

        lines.forEach((line, index) => {
            const needle = lineSearchNeedle(line);
            if (!needle) {
                return;
            }

            let matchedPage = null;
            for (let offset = 0; offset < pageNumbers.length; offset++) {
                const pageIndex = (preferredPageIndex + offset) % pageNumbers.length;
                const pageNumber = pageNumbers[pageIndex];
                const pageText = pageTextMap.get(pageNumber) || '';
                if (pageText.includes(needle)) {
                    matchedPage = pageNumber;
                    preferredPageIndex = pageIndex;
                    break;
                }
            }

            if (!matchedPage) {
                return;
            }

            const lineNumber = index + 1;
            lineToPageMap.set(lineNumber, matchedPage);
            const pageLines = pageToLinesMap.get(matchedPage) || [];
            pageLines.push(lineNumber);
            pageToLinesMap.set(matchedPage, pageLines);
        });
    }

    function closestMappedPage(lineNumber) {
        if (lineToPageMap.has(lineNumber)) {
            return lineToPageMap.get(lineNumber);
        }

        let closestLine = null;
        let closestDistance = Infinity;

        lineToPageMap.forEach((_, mappedLine) => {
            const distance = Math.abs(mappedLine - lineNumber);
            if (distance < closestDistance) {
                closestDistance = distance;
                closestLine = mappedLine;
            }
        });

        if (closestLine === null || closestDistance > 30) {
            return null;
        }

        return lineToPageMap.get(closestLine);
    }

    function closestMappedLine(pageNumber, positionRatio = 0.5) {
        const exactPageLine = pickLineFromCandidates(pageToLinesMap.get(pageNumber), positionRatio);
        if (exactPageLine !== null) {
            return exactPageLine;
        }

        let closestPage = null;
        let closestDistance = Infinity;

        pageToLinesMap.forEach((_, mappedPage) => {
            const distance = Math.abs(mappedPage - pageNumber);
            if (distance < closestDistance) {
                closestDistance = distance;
                closestPage = mappedPage;
            }
        });

        if (closestPage === null || closestDistance > 2) {
            return null;
        }

        return pickLineFromCandidates(pageToLinesMap.get(closestPage), positionRatio);
    }

    function estimatePageFromLine(lineNumber) {
        if (!pdfDocument || !compileTarget) {
            return null;
        }

        const source = fileContents[compileTarget] || '';
        const lines = source.split(/\r?\n/);
        const lineCount = lines.length;
        const pageCount = pdfDocument.numPages || 0;
        if (!lineCount || !pageCount) {
            return null;
        }

        const safeLine = clampLineValue(lineNumber, 1, lineCount);
        const ratio = (safeLine - 1) / Math.max(lineCount - 1, 1);
        return clampLineValue(Math.round(ratio * Math.max(pageCount - 1, 0)) + 1, 1, pageCount);
    }

    function estimateLineFromPage(pageNumber, positionRatio = 0.5) {
        if (!pdfDocument || !compileTarget) {
            return null;
        }

        const source = fileContents[compileTarget] || '';
        const lines = source.split(/\r?\n/);
        const lineCount = lines.length;
        const pageCount = pdfDocument.numPages || 0;
        if (!lineCount || !pageCount) {
            return null;
        }

        const safePage = clampLineValue(pageNumber, 1, pageCount);
        const safePositionRatio = clampUnitInterval(positionRatio);
        const ratio = ((safePage - 1) + safePositionRatio) / pageCount;
        return clampLineValue(Math.round(ratio * Math.max(lineCount - 1, 0)) + 1, 1, lineCount);
    }

    function setActivePDFPage(pageNumber, scrollIntoView = true) {
        const current = document.querySelector('#pdf-pages .pdf-page-active');
        if (current) {
            current.classList.remove('pdf-page-active');
        }

        const pageEl = document.querySelector(`#pdf-pages .pdf-page[data-page="${pageNumber}"]`);
        if (!pageEl) {
            return;
        }

        pageEl.classList.add('pdf-page-active');
        activePDFPage = pageNumber;

        if (scrollIntoView) {
            pageEl.scrollIntoView({ behavior: 'smooth', block: 'center' });
        }
    }

    function highlightEditorRange(startLine, endLine = startLine) {
        const model = editor ? editor.getModel() : null;
        if (!model) {
            return;
        }
        const lineCount = model.getLineCount();
        const safeStart = clampLineValue(startLine, 1, lineCount);
        const safeEnd = clampLineValue(endLine, safeStart, lineCount);

        lineHighlightDecorations = editor.deltaDecorations(lineHighlightDecorations, [
            {
                range: new monaco.Range(safeStart, 1, safeEnd, 1),
                options: {
                    isWholeLine: true,
                    className: 'source-line-highlight',
                },
            },
        ]);

        if (lineHighlightTimer) {
            clearTimeout(lineHighlightTimer);
        }
        lineHighlightTimer = setTimeout(() => {
            lineHighlightDecorations = editor.deltaDecorations(lineHighlightDecorations, []);
        }, 1200);
    }

    function highlightEditorLine(lineNumber) {
        highlightEditorRange(lineNumber, lineNumber);
    }

    function showLineInPDF(lineNumber, withMessage = false) {
        if (!pdfDocument) {
            if (withMessage) {
                setStatus('Compile to generate PDF preview first', true);
            }
            return;
        }

        let pageNumber = closestMappedPage(lineNumber);
        const approximate = !pageNumber;
        if (!pageNumber) {
            pageNumber = estimatePageFromLine(lineNumber);
        }
        if (!pageNumber) {
            if (withMessage) {
                setStatus('No PDF sync point found for this line', true);
            }
            return;
        }

        setActivePDFPage(pageNumber, true);
        if (withMessage) {
            if (approximate) {
                setStatus(`Approximate sync: ${compileTarget}:${lineNumber} -> PDF page ${pageNumber}`);
            } else {
                setStatus(`${compileTarget}:${lineNumber} -> PDF page ${pageNumber}`);
            }
        }
    }

    function showCurrentLineInPDF() {
        if (!editor) {
            return;
        }

        if (!compileTarget) {
            setStatus('Select a compile entry first', true);
            return;
        }

        if (currentFile !== compileTarget) {
            setStatus(`Open ${compileTarget} to sync cursor with PDF`, true);
            return;
        }

        const position = editor.getPosition();
        if (!position) {
            return;
        }
        showLineInPDF(position.lineNumber, true);
    }

    async function jumpToSourceFromPage(pageNumber, event = null) {
        let positionRatio = 0.5;
        if (event && event.currentTarget) {
            const rect = event.currentTarget.getBoundingClientRect();
            if (rect.height > 0) {
                positionRatio = (event.clientY - rect.top) / rect.height;
            }
        }

        let lineNumber = closestMappedLine(pageNumber, positionRatio);
        const approximate = !lineNumber;
        if (!lineNumber) {
            lineNumber = estimateLineFromPage(pageNumber, positionRatio);
        }
        if (!lineNumber) {
            setStatus('No source sync point found for this PDF page', true);
            return;
        }

        if (!compileTarget) {
            setStatus('Select a compile entry first', true);
            return;
        }

        await openFile(compileTarget);

        const maxLine = editor.getModel().getLineCount();
        const safeLine = Math.min(Math.max(lineNumber, 1), maxLine);

        suppressEditorPDFSync = true;
        editor.revealLineInCenter(safeLine);
        editor.setPosition({ lineNumber: safeLine, column: 1 });
        editor.focus();
        highlightEditorLine(safeLine);
        setTimeout(() => {
            suppressEditorPDFSync = false;
        }, 0);

        setActivePDFPage(pageNumber, false);
        if (approximate) {
            setStatus(`Approximate sync: PDF page ${pageNumber} -> ${compileTarget}:${safeLine}`);
        } else {
            setStatus(`PDF page ${pageNumber} -> ${compileTarget}:${safeLine}`);
        }
    }

    async function renderPDF(data) {
        if (pdfDocument) {
            await pdfDocument.destroy();
            pdfDocument = null;
        }

        pdfDocument = await pdfjsLib.getDocument({ data, isEvalSupported: false }).promise;
        document.getElementById('pdf-toolbar').classList.remove('hidden');
        updateZoomLabel();
        await renderPDFPages();
    }

    async function renderPDFPages() {
        if (!pdfDocument) {
            return;
        }

        const renderToken = ++pdfRenderToken;
        const container = document.getElementById('pdf-container');
        const pagesContainer = document.getElementById('pdf-pages');
        const placeholder = document.getElementById('pdf-placeholder');

        pagesContainer.innerHTML = '';
        pageTextMap = new Map();
        lineToPageMap = new Map();
        pageToLinesMap = new Map();

        const containerWidth = Math.max(300, container.clientWidth - 56);
        const devicePixelRatio = window.devicePixelRatio || 1;

        for (let i = 1; i <= pdfDocument.numPages; i++) {
            if (renderToken !== pdfRenderToken) {
                return;
            }

            const page = await pdfDocument.getPage(i);
            const baseViewport = page.getViewport({ scale: 1 });
            const scale = (containerWidth / baseViewport.width) * pdfZoom;
            const scaledViewport = page.getViewport({ scale });

            const pageWrapper = document.createElement('div');
            pageWrapper.className = 'pdf-page rounded-md overflow-hidden';
            pageWrapper.dataset.page = String(i);
            pageWrapper.addEventListener('click', (event) => jumpToSourceFromPage(i, event));

            const canvas = document.createElement('canvas');
            canvas.width = Math.ceil(scaledViewport.width * devicePixelRatio);
            canvas.height = Math.ceil(scaledViewport.height * devicePixelRatio);
            canvas.style.width = `${scaledViewport.width}px`;
            canvas.style.height = `${scaledViewport.height}px`;
            canvas.className = 'block';

            pageWrapper.appendChild(canvas);
            pagesContainer.appendChild(pageWrapper);

            const ctx = canvas.getContext('2d');
            await page.render({
                canvasContext: ctx,
                viewport: scaledViewport,
                transform: [devicePixelRatio, 0, 0, devicePixelRatio, 0, 0],
            }).promise;

            const textContent = await page.getTextContent();
            const pageText = textContent.items.map(item => item.str).join(' ');
            pageTextMap.set(i, normalizePDFText(pageText));
        }

        if (renderToken !== pdfRenderToken) {
            return;
        }

        buildLinePageMaps();

        if (activePDFPage !== null) {
            setActivePDFPage(activePDFPage, false);
        }

        placeholder.classList.add('hidden');
        pagesContainer.classList.remove('hidden');
    }

    window.addEventListener('resize', () => {
        if (!pdfDocument) {
            return;
        }

        if (pdfResizeTimer) {
            clearTimeout(pdfResizeTimer);
        }
        pdfResizeTimer = setTimeout(() => {
            renderPDFPages();
        }, 120);
    });

    document.getElementById('pdf-zoom-out').addEventListener('click', () => setPDFZoom(pdfZoom - 0.1));
    document.getElementById('pdf-zoom-in').addEventListener('click', () => setPDFZoom(pdfZoom + 0.1));
    document.getElementById('pdf-zoom-reset').addEventListener('click', () => setPDFZoom(1));
    document.getElementById('pdf-fit-width').addEventListener('click', () => setPDFZoom(1));
    document.getElementById('download-source-btn').addEventListener('click', () => {
        closeDownloadDropdown();
        downloadSourceArchive();
    });
    document.getElementById('download-pdf-btn').addEventListener('click', () => {
        closeDownloadDropdown();
        downloadCompiledPDF();
    });

    function toggleDownloadDropdown() {
        const menu = document.getElementById('download-dropdown-menu');
        menu.classList.toggle('hidden');
    }

    function closeDownloadDropdown() {
        const menu = document.getElementById('download-dropdown-menu');
        menu.classList.add('hidden');
    }

    document.getElementById('download-dropdown-btn').addEventListener('click', (event) => {
        event.stopPropagation();
        toggleDownloadDropdown();
    });

    document.addEventListener('click', (event) => {
        const dropdown = document.getElementById('download-dropdown');
        if (!dropdown.contains(event.target)) {
            closeDownloadDropdown();
        }
    });
    document.getElementById('save-btn').addEventListener('click', save);
    document.getElementById('view-editor-btn').addEventListener('click', () => applyWorkspaceView(WORKSPACE_VIEW_EDITOR));
    document.getElementById('view-pdf-btn').addEventListener('click', () => applyWorkspaceView(WORKSPACE_VIEW_PDF));
    document.getElementById('view-split-btn').addEventListener('click', () => applyWorkspaceView(WORKSPACE_VIEW_SPLIT));
    document.getElementById('compile-btn').addEventListener('click', compile);
    document.getElementById('sidebar-files-btn').addEventListener('click', () => selectSidebarPanel('files'));
    document.getElementById('sidebar-comments-btn').addEventListener('click', () => selectSidebarPanel('comments'));
    document.getElementById('sidebar-git-btn').addEventListener('click', () => selectSidebarPanel('git'));
    document.getElementById('sidebar-settings-btn').addEventListener('click', () => selectSidebarPanel('settings'));
    document.getElementById('sidebar-collapse-btn').addEventListener('click', toggleSidebarCollapsed);
    applyWritePermissions();
    updateGitUI(gitStatus);
    setSaveEnabled(canWrite);
    const gitConfigForm = document.getElementById('git-config-form');
    if (gitConfigForm) {
        gitConfigForm.addEventListener('submit', saveGitConfig);
    }
    const gitPullButton = document.getElementById('git-pull-btn');
    if (gitPullButton) {
        gitPullButton.addEventListener('click', gitPull);
    }
    const gitPushButton = document.getElementById('git-push-btn');
    if (gitPushButton) {
        gitPushButton.addEventListener('click', gitPush);
    }
    const gitResetButton = document.getElementById('git-reset-btn');
    if (gitResetButton) {
        gitResetButton.addEventListener('click', gitReset);
    }
    const gitRebaseButton = document.getElementById('git-rebase-btn');
    if (gitRebaseButton) {
        gitRebaseButton.addEventListener('click', gitPullRebase);
    }
    const gitDiscardButton = document.getElementById('git-discard-btn');
    if (gitDiscardButton) {
        gitDiscardButton.addEventListener('click', gitDiscardLocalCommits);
    }
    document.getElementById('git-changed-files-toggle').addEventListener('click', () => {
        const list = document.getElementById('git-changed-files-list');
        const chevron = document.getElementById('git-changed-files-chevron');
        list.classList.toggle('hidden');
        chevron.style.transform = list.classList.contains('hidden') ? '' : 'rotate(90deg)';
    });
    const gitConfigToggle = document.getElementById('git-config-toggle');
    if (gitConfigToggle) {
        gitConfigToggle.addEventListener('click', () => {
            const form = document.getElementById('git-config-form');
            const chevron = document.getElementById('git-config-chevron');
            form.classList.toggle('hidden');
            chevron.style.transform = form.classList.contains('hidden') ? '' : 'rotate(90deg)';
        });
    }
    document.getElementById('comment-scope-all').addEventListener('change', renderCommentList);
    document.getElementById('comment-use-selection-btn').addEventListener('click', () => updateCommentTargetFromEditor(true));
    document.getElementById('comment-add-btn').addEventListener('click', addComment);
    document.getElementById('comment-input').addEventListener('keydown', (event) => {
        if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
            event.preventDefault();
            addComment();
        }
    });
    applySidebarPanel(readStoredSidebarPanel(), false);
    applySidebarCollapsed(readStoredSidebarCollapsed(), false);
    applyWorkspaceView(readStoredWorkspaceView(), false);
    updateCommentTargetLabel();
    updateZoomLabel();
    updateCompileStat();
