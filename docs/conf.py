import os
import sys
# This line tells Sphinx to look two levels up for your code 
# (assuming your structure is /project/docs/conf.py)
sys.path.insert(0, os.path.abspath('..'))

# -- Project information -----------------------------------------------------
project = 'ariadna-self-healing'
copyright = '2026, Your Name or Org'
author = 'Your Name'
release = '0.1.0'

# -- General configuration ---------------------------------------------------
extensions = [
    'sphinx.ext.autodoc',      # Pulls documentation from docstrings
    'sphinx.ext.viewcode',     # Adds links to highlighted source code
    'sphinx.ext.napoleon',     # Support for Google/NumPy style docstrings
    'myst_parser',             # Allows you to use .md files alongside .rst
]

templates_path = ['_templates']
exclude_patterns = ['_build', 'Thumbs.db', '.DS_Store']

# -- Options for HTML output -------------------------------------------------
# The "Read the Docs" theme is standard and responsive
html_theme = 'sphinx_rtd_theme'
html_static_path = ['_static']

# Ensure the master doc is index
master_doc = 'index'
