import type { KnipConfig } from 'knip';
const config: KnipConfig = {
  entry: ['tests/**/*.test.{ts,mjs}', 'operator/build.mjs', 'native-smoke/inspect-app-startup.ts'],
  project: ['src/**/*.{ts,tsx}!', 'tests/**/*.{ts,mjs}', 'scripts/**/*.mjs', 'native-smoke/**/*.ts', 'operator/*.{ts,mjs}', 'host-status-fixture/*.ts', '*.{ts,mjs}'],
  vite: { config: ['vite.config.ts', 'operator/vite.config.ts', 'host-status-fixture/vite.config.ts'] }
};
export default config;
