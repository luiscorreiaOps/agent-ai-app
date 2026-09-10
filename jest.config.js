/** @type {import('jest').Config} */
module.exports = {
  testEnvironment: 'jsdom',
  roots: ['<rootDir>/src'],
  transform: {
    // .jsx? too, not just .tsx? -- some allowlisted node_modules packages
    // (e.g. marked, pulled in transitively by @grafana/data) ship ESM .js
    // files that need transpiling to CJS same as our own TS, otherwise
    // Jest hits a raw `export` token the moment anything imports them.
    '^.+\\.[jt]sx?$': ['@swc/jest'],
  },
  moduleFileExtensions: ['ts', 'tsx', 'js', 'jsx', 'json'],
  setupFilesAfterEnv: ['<rootDir>/src/jest-setup.ts'],
  testPathIgnorePatterns: ['/node_modules/'],
  transformIgnorePatterns: [
    // d3-.* as a prefix, not each package named individually -- @grafana/data's
    // root export (field color scales, pulled in the moment anything imports
    // AppEvents alongside it) transitively needs more d3-* packages than were
    // ever enumerated here, and the previous exact-name list broke the first
    // time a new one (d3-scale-chromatic) showed up in the graph.
    '/node_modules/(?!(@grafana|d3-.*|internmap|ol|geotiff|quick-lru|rehype-.*|mdast-util-.*|micromark.*|unist-util-.*|unified|bail|trough|vfile.*|devlop|property-information|hast-util-.*|space-separated-tokens|comma-separated-tokens|estree-util-.*|ccount|escape-string-regexp|markdown-table|longest-streak|zwitch|html-void-elements|web-namespaces|stringify-entities|character-entities.*|is-plain-obj|trim-lines|parse5|direction|decode-named-character-reference|character-reference-invalid|is-decimal|is-hexadecimal|is-alphanumerical|is-alphabetical|marked|react-calendar|get-user-locale|memoize|mimic-function)/)',
  ],
  moduleNameMapper: {
    '\\.(css|less|scss|sass)$': '<rootDir>/src/__mocks__/styleMock.js',
    '\\.(gif|ttf|eot|svg|png)$': '<rootDir>/src/__mocks__/fileMock.js',
  },
};
