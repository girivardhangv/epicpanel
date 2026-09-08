/** @type {import('tailwindcss').Config} */
export default {
  content: [
    './index.html',
    './src/**/*.{ts,tsx}',
    './packages/**/*.{ts,tsx}',
    './apps/**/*.{ts,tsx,html}',
  ],
  theme: {
    extend: {
      colors: {
        app: '#f5f7fb',
        surface: { DEFAULT: '#ffffff', 2: '#f8fafc' },
        sidebar: {
          DEFAULT: '#0c1526',
          2: '#111e32',
          hover: 'rgba(52, 106, 255, .2)',
          text: '#aebbd0',
        },
        line: { DEFAULT: '#e7ebf2', soft: '#eef1f6' },
        brand: { DEFAULT: '#2563eb', dark: '#1d4ed8', soft: '#eef4ff' },
        accent: { DEFAULT: '#2563eb', dark: '#1d4ed8' },
        ok: { DEFAULT: '#0f9d6e', soft: '#eaf9f3' },
        warn: { DEFAULT: '#d88b00', soft: '#fff7e7' },
        danger: { DEFAULT: '#dc3d4b', soft: '#fff0f2' },
        purple: { DEFAULT: '#7c4dff', soft: '#f2edff' },
        ink: '#172033',
        sub: '#596579',
        muted: '#667085',
        navy: '#132039',
      },
      boxShadow: {
        card: '0 2px 10px rgba(24, 39, 75, 0.05)',
        pop: '0 8px 30px rgba(24, 39, 75, 0.06)',
        toast: '0 20px 40px rgba(13, 24, 43, 0.22)',
        modal: '0 30px 80px rgba(15, 23, 42, 0.25)',
      },
      borderRadius: {
        DEFAULT: '9px',
        card: '14px',
      },
      fontFamily: {
        sans: ['Inter', 'ui-sans-serif', 'system-ui', '-apple-system', 'BlinkMacSystemFont', 'Segoe UI', 'sans-serif'],
      },
    },
  },
  plugins: [],
}
