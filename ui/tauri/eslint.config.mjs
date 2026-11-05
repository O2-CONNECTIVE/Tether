import js from '@eslint/js';
import { defineConfig, globalIgnores } from 'eslint/config';
import tseslint from 'typescript-eslint';
import hooks from 'eslint-plugin-react-hooks';
import globals from 'globals';

export default defineConfig(
  globalIgnores(['dist/**', 'operator/dist/**', 'host-status-fixture/dist/**', 'src-tauri/**']),
  { files: ['src/**/*.{ts,tsx}', 'tests/**/*.ts', 'native-smoke/**/*.ts', 'operator/*.ts', 'host-status-fixture/*.ts', '*.ts'],
    extends: [js.configs.recommended, tseslint.configs.recommendedTypeChecked],
    languageOptions: { globals: { ...globals.browser, ...globals.node }, parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname } } },
  { files: ['src/**/*.tsx'], extends: [hooks.configs.flat.recommended] },
  { files: ['src/**/*.{ts,tsx}'], ignores: ['src/main.tsx'], rules: {
    'no-restricted-imports': ['error', { patterns: [{ group: ['@tauri-apps/*', 'node:*'], message: 'Inject IO at bootstrap; screen/controller must not import native IO.' }] }],
    'no-restricted-properties': ['error', { object: 'window', property: '__TAURI__', message: 'Native adapter belongs in main.tsx only.' }]
  } },
  { files: ['**/*.mjs'], extends: [js.configs.recommended], languageOptions: { globals: { ...globals.node, ...globals.browser } } }
);
