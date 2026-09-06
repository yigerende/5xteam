/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{vue,js}'],
  darkMode: ['class', '[data-theme="dark"]'],
  theme: {
    extend: {
      colors: {
        primary: {
          50: '#f0fdfa', 100: '#ccfbf1', 200: '#99f6e4', 300: '#5eead4',
          400: '#2dd4bf', 500: '#14b8a6', 600: '#0d9488', 700: '#0f766e',
          800: '#115e59', 900: '#134e4a', 950: '#042f2e',
        },
      },
      boxShadow: {
        card: '0 1px 3px rgba(15, 23, 42, .04), 0 8px 24px rgba(15, 23, 42, .05)',
        'card-hover': '0 14px 36px rgba(15, 23, 42, .09)',
      },
    },
  },
  plugins: [],
}
